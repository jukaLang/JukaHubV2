package main

// sleephooks.go — places in the sleep path (powersleep.go, powerSleepRun)
// where a feature can take part without editing it.
//
//   - sleepHookRegisterBefore: runs on the sleep goroutine right after
//     "going to sleep" is logged, before the running game is stopped
//     (powerSleepHold), the backlight goes off and the card is synced. The
//     game is still running, so a hook can still talk to it. The screen is
//     still lit and the person is waiting for it to go dark, so the hooks
//     together get sleepHookBeforeBudget: a hook that starts after the budget
//     is spent is skipped, and the skip is logged.
//   - sleepHookRegisterWake: runs on the sleep goroutine at the very end of
//     the wake, after the children the sleep stopped were continued. slept is
//     how long the kernel was suspended (summed over the travel lock's
//     re-sleeps); suspended is false when the kernel never suspended (the
//     screen-off sleep, a refused or spurious suspend). It also runs when the
//     power-off deadline's wake is turning the device off: check
//     quickPowerOffSilent() or exitRequested before starting anything long.
//
// Register from init(). A hook's panic is logged and swallowed, so one feature
// never leaves the screen dark or the game stopped.

import (
	"sync"
	"time"
)

// sleepHookBeforeBudget bounds the before-sleep hooks together.
const sleepHookBeforeBudget = 3 * time.Second

type (
	sleepBeforeFunc func(source string)
	sleepWakeFunc   func(source string, slept time.Duration, suspended bool)
)

var sleepHooks struct {
	mu     sync.RWMutex
	before []sleepBeforeFunc
	wake   []sleepWakeFunc
}

func sleepHookRegisterBefore(fn sleepBeforeFunc) {
	if fn == nil {
		return
	}
	sleepHooks.mu.Lock()
	sleepHooks.before = append(sleepHooks.before, fn)
	sleepHooks.mu.Unlock()
}

func sleepHookRegisterWake(fn sleepWakeFunc) {
	if fn == nil {
		return
	}
	sleepHooks.mu.Lock()
	sleepHooks.wake = append(sleepHooks.wake, fn)
	sleepHooks.mu.Unlock()
}

// sleepHooksRunBefore runs the before-sleep hooks in order within the budget.
func sleepHooksRunBefore(source string) {
	sleepHooks.mu.RLock()
	hooks := sleepHooks.before
	sleepHooks.mu.RUnlock()
	start := time.Now()
	for i, fn := range hooks {
		if time.Since(start) >= sleepHookBeforeBudget {
			logWarn("sleep", "skipped %d before-sleep step(s): the %s budget was spent", len(hooks)-i, sleepHookBeforeBudget)
			return
		}
		sleepHookCall(func() { fn(source) })
	}
}

// sleepHooksRunWake runs the wake hooks in order.
func sleepHooksRunWake(source string, slept time.Duration, suspended bool) {
	sleepHooks.mu.RLock()
	hooks := sleepHooks.wake
	sleepHooks.mu.RUnlock()
	for _, fn := range hooks {
		sleepHookCall(func() { fn(source, slept, suspended) })
	}
}

func sleepHookCall(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("sleep", "a sleep step failed: %v", r)
		}
	}()
	fn()
}
