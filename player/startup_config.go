package main

// startup_config.go — the design config is decoded while SDL starts.
//
// jukaconfig.json is close to 100 KB of scenes that the custom element and
// variable decoders walk more than once: tens of milliseconds of one A53 core.
// sdl.Init, the safe-mode checks and the boot guard's durable write wait on the
// GPU driver and the card, not on that core, so the decode runs beside them on
// its own goroutine and main.go takes the result where it used to load it.
//
// The order the boot guard relies on is unchanged: a decode that panics is
// re-raised on the main goroutine at the hand-over, after the guard has counted
// this start, so a config that crashes the loader is still a counted failed
// start (and three of them still start safe mode). The loader touches no SDL
// object, and the only state it shares is configHealth (behind its own mutex)
// and the recovery notice, which nothing reads before the hand-over.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
)

type startupConfigResult struct {
	config *Config
	path   string
	err    error
	panic  any
	stack  []byte
}

// startupLoadConfigAsync starts LoadXMLConfigWithFallback(jsonPath) now and
// returns the hand-over, which waits for it. Call the hand-over once, on the
// main goroutine.
func startupLoadConfigAsync(jsonPath string) func() (*Config, string, error) {
	done := make(chan startupConfigResult, 1)
	go func() {
		var res startupConfigResult
		defer func() {
			if r := recover(); r != nil {
				res = startupConfigResult{panic: r, stack: debug.Stack()}
			}
			done <- res
		}()
		res.config, res.path, res.err = LoadXMLConfigWithFallback(jsonPath)
	}()
	return func() (*Config, string, error) {
		res := <-done
		if res.panic != nil {
			panic(fmt.Sprintf("loading %s: %v\n\n%s", jsonPath, res.panic, res.stack))
		}
		return res.config, res.path, res.err
	}
}

// startupFindHome makes the executable's folder the working directory when
// the folder JukaHub was started in holds no jukaconfig.json (or .xml) but
// that one does, and returns the folder it left ("" when it stayed). Every
// relative path JukaHub reads and writes — the design config, jukauser.json,
// the themes — is then right whatever started it: Windows' Startup folder or
// Run key, a shortcut without "Start in", a terminal in another folder. A
// folder with a config of its own is never left, so a launcher that chose one
// (launch.sh) or a developer's checkout keeps it. Called before logging
// starts, which is why the caller logs the move.
func startupFindHome() string {
	if startupHasConfig(".") {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	if !startupHasConfig(dir) {
		return ""
	}
	was, _ := os.Getwd()
	if os.Chdir(dir) != nil {
		return ""
	}
	if was == "" {
		was = "?"
	}
	return was
}

// startupHasConfig reports whether dir holds the design config, in either
// format (xmlconfig.go).
func startupHasConfig(dir string) bool {
	for _, name := range []string{"jukaconfig.json", "jukaconfig.xml"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}
