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
