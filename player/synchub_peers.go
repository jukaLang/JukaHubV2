package main

// synchub_peers.go — continuity between paired JukaHubs with nothing pressed.
//
// Save sync between two paired devices used to run only from its page:
// Compare, then Sync now. With Automatic sync on (synchub.go) the paired
// devices on this Wi-Fi take part in the same moments as the clouds
// (savesync_auto.go):
//
//   - before a library game starts, a paired device whose copy of the game's
//     saves is newer hands it over (savehandoff.go's per-game comparison: the
//     version both devices recorded at their last sync decides, never the
//     clocks), inside the launch budget the clouds share;
//   - after the game ends, saves newer here go to the paired devices on this
//     Wi-Fi (one that is in a game itself refuses every write, so it is simply
//     left for later);
//   - half a minute later the background pass runs Save sync's whole
//     comparison with each of them, which is also what records that version
//     for the files both already hold, so the next game's handover is sure.
//
// It follows Device Sync's Saves and Save states switches. What it never
// does: pick between two copies that both changed (that stays the Sync page's
// Compare, and a toast says so), act on a comparison only file times ordered,
// wait for a device that is not on this Wi-Fi, or move ROMs, BIOS, collections
// and settings — those stay Device Sync's, on the page.

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// syncPeersMax bounds how many paired devices one game asks.
	syncPeersMax = 3
	// syncPeersQuiet: a device that copied files with this one this recently
	// (its own sync of this device) is left alone by the background pass.
	syncPeersQuiet = 90 * time.Second
	// syncPeersSpread is the most the background pass waits first, so two
	// devices whose games ended together do not start syncing each other in
	// the same instant.
	syncPeersSpread = 6 * time.Second
)

// syncPeersBusy marks the paired devices this device is running a whole sync
// with. While it runs, the same device's own sync with this one is refused
// (ssyncHandleManifest answers 409): two devices never sync each other in
// both directions at once, and the one refused tries again next time.
var syncPeersBusy struct {
	mu sync.Mutex
	m  map[string]bool
}

func syncPeersSetBusy(id string, on bool) {
	syncPeersBusy.mu.Lock()
	defer syncPeersBusy.mu.Unlock()
	if syncPeersBusy.m == nil {
		syncPeersBusy.m = map[string]bool{}
	}
	if on {
		syncPeersBusy.m[id] = true
		return
	}
	delete(syncPeersBusy.m, id)
}

// syncPeersBusyWith reports whether this device is syncing with id right now
// (any goroutine).
func syncPeersBusyWith(id string) bool {
	syncPeersBusy.mu.Lock()
	defer syncPeersBusy.mu.Unlock()
	return syncPeersBusy.m[id]
}

// ssyncIncomingRecent reports whether paired device id copied files to or
// from this device within d (a sync it ran itself).
func ssyncIncomingRecent(id string, d time.Duration) bool {
	ssyncIncoming.mu.Lock()
	defer ssyncIncoming.mu.Unlock()
	now := time.Now()
	for k, s := range ssyncIncoming.sessions {
		if strings.HasPrefix(k, id+"|") && now.Sub(s.seen) < d {
			return true
		}
	}
	return false
}

// syncPeersAutoReady reports whether the automatic syncs include the paired
// devices: Automatic sync is on, the peer layer runs and at least one paired
// device has a known address (asking it costs no scan). Any goroutine.
func syncPeersAutoReady() bool {
	return syncAutoFlag.Load() && peerEnabledFlag.Load() && len(shoReachablePeers()) > 0
}

// syncPeersLaunchReady reports whether a game's launch should wait for its
// saves from the paired devices.
func syncPeersLaunchReady(game libraryGame) bool {
	return shoApplies(game) && syncPeersAutoReady()
}

// syncPeersGame is the game as the save handoff finds its files; false for a
// game whose saves are not an emulator's (ports, Android, web games).
func syncPeersGame(romPath, system string) (shoGame, bool) {
	game := libraryGame{Path: romPath, System: system}
	if !shoApplies(game) {
		return shoGame{}, false
	}
	return shoGameOf(game), true
}

// syncPeersCheck compares one game with one paired device, leaving out the
// save or state folders Device Sync's switches turn off (as Save sync does,
// devsyncFilterSaves).
func syncPeersCheck(ctx context.Context, g shoGame, peer peerRecord) (shoComparison, error) {
	cmp, err := shoCheck(ctx, g, peer, true)
	if err != nil {
		return cmp, err
	}
	saves, states := devsyncCatOn(dsCatSaves), devsyncCatOn(dsCatStates)
	if saves && states {
		return cmp, nil
	}
	names := map[string]string{}
	for _, r := range cmp.remote.Roots {
		names[r.Key] = r.Name
	}
	for _, r := range cmp.local.Roots {
		names[r.Key] = r.Name
	}
	cmp.plan = ssyncFilterPlan(cmp.plan, func(it ssyncItem) bool {
		if devsyncIsStatesRoot(names[it.Root]) {
			return states
		}
		return saves
	})
	return cmp, nil
}

// syncPeersBothChanged is the note for saves changed on both sides.
func syncPeersBothChanged(name string) string {
	return "Saves changed here and on " + name + " — Sync › " + name + " › Compare picks one"
}

// syncPeersBeforeLaunch brings the game's saves from a paired device that
// has them newer, before the game starts here. It returns the toast's notes
// and whether one of them is a warning. ctx is the launch budget: a copy it
// cuts short stops before the game starts, never after.
func syncPeersBeforeLaunch(ctx context.Context, romPath, system string) (notes []string, warn bool) {
	g, ok := syncPeersGame(romPath, system)
	if !ok || !syncPeersAutoReady() {
		return nil, false
	}
	for i, peer := range shoReachablePeers() {
		if i >= syncPeersMax || ctx.Err() != nil {
			break
		}
		cmp, err := syncPeersCheck(ctx, g, peer)
		if err != nil {
			logInfo("synchub", "before %s: no answer from %s: %v", g.stem, peer.Name, err)
			continue
		}
		p := cmp.plan
		switch {
		case p.Conflict > 0 || (p.Pull > 0 && p.Push > 0):
			warn = true
			notes = append(notes, syncPeersBothChanged(peer.Name))
		case p.Pull > 0 && cmp.Sure:
			res, err := shoRunCtx(ctx, cmp, false, nil)
			if err != nil {
				logInfo("synchub", "before %s: getting from %s: %v", g.stem, peer.Name, err)
				continue
			}
			if res.Pulled+res.Conflicts > 0 {
				notes = append(notes, "Saves updated from "+peer.Name)
			}
			if res.Failed > 0 {
				warn = true
				notes = append(notes, syncPeersFailedNote(res, peer.Name))
			}
		case p.Pull > 0:
			// Only file times say which is newer: the background pass records
			// the version both hold, and Compare decides now.
			notes = append(notes, "Saves may be newer on "+peer.Name+" — Sync › "+peer.Name+" › Compare checks")
		}
	}
	return notes, warn
}

// syncPeersAfterExit sends the game's saves to the paired devices on this
// Wi-Fi once it has ended here, where they are newer here. It returns the
// toast's notes and whether one of them is a warning.
func syncPeersAfterExit(ctx context.Context, romPath, system string) (notes []string, warn bool) {
	g, ok := syncPeersGame(romPath, system)
	if !ok || !syncPeersAutoReady() {
		return nil, false
	}
	var sent []string
	for i, peer := range shoReachablePeers() {
		if i >= syncPeersMax || ctx.Err() != nil {
			break
		}
		cmp, err := syncPeersCheck(ctx, g, peer)
		if err != nil {
			// Away, asleep or in a game: the next launch there asks again.
			logInfo("synchub", "after %s: no answer from %s: %v", g.stem, peer.Name, err)
			continue
		}
		p := cmp.plan
		switch {
		case p.Conflict > 0 || (p.Pull > 0 && p.Push > 0):
			warn = true
			notes = append(notes, syncPeersBothChanged(peer.Name))
		case p.Push > 0 && cmp.Sure:
			res, err := shoRunCtx(ctx, cmp, true, nil)
			if err != nil {
				logInfo("synchub", "after %s: sending to %s: %v", g.stem, peer.Name, err)
				continue
			}
			if res.Pushed+res.Conflicts > 0 {
				sent = append(sent, peer.Name)
			}
			if res.Failed > 0 {
				warn = true
				notes = append(notes, syncPeersFailedNote(res, peer.Name))
			}
		}
	}
	if len(sent) > 0 {
		notes = append([]string{"Saves sent to " + strings.Join(sent, " and ")}, notes...)
	}
	return notes, warn
}

// syncPeersFailedNote says what a game's handover could not copy.
func syncPeersFailedNote(res ssyncResult, name string) string {
	msg := pluralizeCount(res.Failed, "save", "saves") + " not copied with " + name
	if len(res.Errors) > 0 {
		msg += ": " + res.Errors[0]
	}
	return msg
}

// syncPeersFull is the background pass (saveSyncRunFull, half a minute after
// a game, or after a pairing): Save sync's whole comparison with each paired
// device on this Wi-Fi, under the page's job slot so the page shows it and B
// there stops it. ctx ends when a game takes the screen. It returns the
// toast's notes and whether one of them is a warning.
func syncPeersFull(ctx context.Context) (notes []string, warn bool) {
	select {
	case <-ctx.Done():
		return nil, false
	case <-time.After(time.Duration(time.Now().UnixNano() % int64(syncPeersSpread))):
	}
	for i, peer := range shoReachablePeers() {
		if i >= 2*syncPeersMax || ctx.Err() != nil {
			break
		}
		if ssyncIncomingRecent(peer.ID, syncPeersQuiet) {
			// It just synced with this device itself.
			continue
		}
		jobCtx, ok := ssyncWorkStart("Syncing saves with " + peer.Name + "…")
		if !ok {
			// The Sync page runs a job of its own: the next pass catches up.
			break
		}
		run, cancel := context.WithCancel(jobCtx)
		stop := context.AfterFunc(ctx, cancel)
		res, err := syncPeerRunFull(run, peer, ssyncWorkProgress)
		stop()
		cancel()
		ssyncWorkEnd()
		if err != nil {
			logInfo("synchub", "background sync with %s: %v", peer.Name, err)
			continue
		}
		logInfo("synchub", "background sync with %s: %d got, %d sent, %d both kept, %d failed",
			peer.Name, res.Pulled, res.Pushed, res.Conflicts, res.Failed)
		id := peer.ID
		tbxPost(func() { syncHubNoteSynced(id) })
		switch {
		case res.Failed > 0 || res.Conflicts > 0:
			warn = true
			notes = append(notes, "Sync with "+peer.Name+": "+ssyncResultText(res))
		case res.Pulled > 0:
			notes = append(notes, pluralizeCount(res.Pulled, "save", "saves")+" updated from "+peer.Name)
		}
	}
	return notes, warn
}

// syncPeerRunFull is Save sync's whole comparison and sync with one paired
// device, with the page's Saves and Save states switches and no questions: a
// file both changed keeps both copies, the newer under the original name.
// Nothing is sent when the two already agree and the record of that is
// already kept, so a quiet pass leaves the other screen quiet too.
func syncPeerRunFull(ctx context.Context, peer peerRecord, progress func(done, total int, current string)) (ssyncResult, error) {
	syncPeersSetBusy(peer.ID, true)
	defer syncPeersSetBusy(peer.ID, false)
	cmp, err := ssyncCompare(ctx, peer.ID, peer.Name)
	if err != nil {
		return ssyncResult{Peer: peer.Name}, err
	}
	cmp.Plan = devsyncFilterSaves(cmp)
	if len(cmp.Plan.Items) == 0 && syncPeersBaseCovers(peer.ID, cmp.Plan.Same) {
		return ssyncResult{Peer: peer.Name, At: time.Now()}, nil
	}
	return ssyncRunSync(ctx, cmp, progress), nil
}

// syncPeersBaseCovers reports whether the last sync with peerID already
// recorded every file both devices hold alike.
func syncPeersBaseCovers(peerID string, same map[string]string) bool {
	base := ssyncLoadBase(peerID)
	for k, sha := range same {
		if !strings.EqualFold(base[k], sha) {
			return false
		}
	}
	return true
}
