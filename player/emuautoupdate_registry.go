package main

// emuautoupdate_registry.go — the emulators and runtimes that the daily
// Emulator updates pass keeps current besides the libretro cores.
//
// Anything JukaHub installs and can bring forward on its own — hakuX on the
// Smart Pro, the Windows emulators it manages on a PC, the Windows-app runtime
// (Box64 + Wine), a downloaded AndroidRT — registers one emuRuntimeUpdater from
// an init() function in its own file. The automatic pass in
// emulatorautoupdate.go walks the registered updaters under the same Emulator
// updates setting as the cores (Off / Notify me / Install automatically) and
// the same rule about the moment (never over a game or another install; on
// the charger or after the user stepped away, unless long overdue).
//
// The contract an updater keeps:
//
//   - Supported and Installed are cheap and never touch the network: they are
//     asked on every look the scheduler takes.
//   - InUse says that replacing the files now would pull them from under a
//     running program (an emulator or app that is open); the pass skips the
//     updater and tries again at its next quiet moment.
//   - Check asks whether a newer build exists and says which ("v0.1.4"); it
//     must not download the build itself.
//   - Install brings it forward, verified the way its manual install is, and
//     keeps whatever the manual install keeps for a rollback. It reports
//     progress as one-line strings, may be cancelled through ctx, and returns
//     an error the pass can log and show in a toast — or errEmuUpdaterCurrent
//     when it found nothing newer after all, which the pass records as a
//     check rather than an update.
//
// The pass (emuautoupdate_runtimes.go) hands Check and Install one client for
// the whole pass: it answers a second look at the same GitHub release from
// memory and holds every GitHub lookup for an hour after a 403 or 429, so use
// the client given rather than a new one.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// errEmuUpdaterCurrent is what an Install may return when the build it found
// is the one already installed: nothing changed, and nothing failed.
var errEmuUpdaterCurrent = errors.New("already the newest version")

// emuRuntimeUpdater is one thing the automatic pass may update.
type emuRuntimeUpdater struct {
	// Key identifies the updater in logs ("hakux", "wine", "androidrt",
	// "win:PCSX2"). A second registration with the same key replaces the
	// first.
	Key string
	// Title names it in toasts and lists ("hakuX", "the Windows app runtime").
	Title string
	// Supported reports whether it applies on this device at all. nil means
	// it does.
	Supported func() bool
	// Installed reports whether there is something installed to update.
	Installed func() bool
	// InUse reports that updating now would replace files a running program
	// uses. nil means never.
	InUse func() bool
	// Check asks whether a newer build exists; label describes it.
	Check func(ctx context.Context, client *http.Client) (newer bool, label string, err error)
	// Install brings it forward; progress (may be nil) receives one-line
	// status updates.
	Install func(ctx context.Context, client *http.Client, progress func(line string)) error
}

// ready reports whether the pass may consider u now (no network involved).
func (u emuRuntimeUpdater) ready() bool {
	if u.Check == nil || u.Install == nil || u.Installed == nil {
		return false
	}
	if u.Supported != nil && !u.Supported() {
		return false
	}
	return u.Installed()
}

// busy reports whether u's program is running.
func (u emuRuntimeUpdater) busy() bool {
	return u.InUse != nil && u.InUse()
}

var emuRuntimeUpdaters struct {
	mu   sync.Mutex
	list []emuRuntimeUpdater
}

// registerEmuRuntimeUpdater adds u to the automatic pass (call it from an
// init function). An updater without a key, a check or an install is ignored.
func registerEmuRuntimeUpdater(u emuRuntimeUpdater) {
	u.Key = strings.TrimSpace(u.Key)
	if u.Key == "" || u.Check == nil || u.Install == nil || u.Installed == nil {
		return
	}
	if strings.TrimSpace(u.Title) == "" {
		u.Title = u.Key
	}
	emuRuntimeUpdaters.mu.Lock()
	defer emuRuntimeUpdaters.mu.Unlock()
	for i := range emuRuntimeUpdaters.list {
		if emuRuntimeUpdaters.list[i].Key == u.Key {
			emuRuntimeUpdaters.list[i] = u
			return
		}
	}
	emuRuntimeUpdaters.list = append(emuRuntimeUpdaters.list, u)
}

// emuRuntimeUpdatersSnapshot returns the registered updaters, ordered by key,
// for one pass.
func emuRuntimeUpdatersSnapshot() []emuRuntimeUpdater {
	emuRuntimeUpdaters.mu.Lock()
	out := make([]emuRuntimeUpdater, len(emuRuntimeUpdaters.list))
	copy(out, emuRuntimeUpdaters.list)
	emuRuntimeUpdaters.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
