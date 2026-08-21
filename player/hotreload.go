package main

import (
	"log"
	"os"
	"sync"
	"time"
)

// ConfigWatcher monitors a config file for changes and triggers a reload.
type ConfigWatcher struct {
	mu       sync.Mutex
	path     string
	lastMod  time.Time
	running  bool
	stopCh   chan struct{}
	reloadCh chan struct{}
	onReload func(*Config)
	debounce *time.Timer
}

// NewConfigWatcher creates a watcher for the given config path.
func NewConfigWatcher(path string) *ConfigWatcher {
	return &ConfigWatcher{
		path:     path,
		stopCh:   make(chan struct{}),
		reloadCh: make(chan struct{}, 1),
	}
}

// SetOnReload sets the callback invoked when a config change is detected.
func (cw *ConfigWatcher) SetOnReload(fn func(*Config)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onReload = fn
}

// Start begins the background file watcher. It polls the file's modification
// time every 2 seconds and debounces changes for 500ms to avoid rapid
// reloads during file writes.
func (cw *ConfigWatcher) Start() {
	cw.mu.Lock()
	if cw.running {
		cw.mu.Unlock()
		return
	}
	cw.running = true
	cw.mu.Unlock()

	// Capture initial modification time
	if info, err := os.Stat(cw.path); err == nil {
		cw.mu.Lock()
		cw.lastMod = info.ModTime()
		cw.mu.Unlock()
	}

	go cw.watchLoop()
	log.Printf("[hotreload] watching %s for changes", cw.path)
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

// watchLoop polls the config file every 2 seconds.
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

// checkFile checks if the config file has been modified since last check.
func (cw *ConfigWatcher) checkFile() {
	info, err := os.Stat(cw.path)
	if err != nil {
		return
	}

	cw.mu.Lock()
	modTime := info.ModTime()
	lastMod := cw.lastMod
	cw.mu.Unlock()

	if !modTime.After(lastMod) {
		return
	}

	cw.mu.Lock()
	cw.lastMod = modTime
	cw.mu.Unlock()

	// Debounce: wait 500ms after the last detected change
	cw.mu.Lock()
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

// performReload loads the config and invokes the callback.
func (cw *ConfigWatcher) performReload() {
	cw.mu.Lock()
	fn := cw.onReload
	cw.mu.Unlock()

	if fn == nil {
		return
	}

	cfg, err := LoadLastKnownGood(cw.path)
	if err != nil {
		log.Printf("[hotreload] reload failed: %v", err)
		return
	}

	// Validate
	if err := NewConfigValidator().Validate(cfg); err != nil {
		log.Printf("[hotreload] validation failed: %v", err)
		return
	}

	fn(cfg)
	log.Printf("[hotreload] config reloaded successfully from %s", cw.path)
}

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
