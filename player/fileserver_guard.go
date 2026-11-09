package main

// fileserver_guard.go — what the FTP and SFTP servers share: the card they
// serve, who is connected, when a client last used them, and the watchdog that
// switches them off (SEC-5).
//
// The servers are for a quick copy at home. Left running they would be a door
// into the card for every device on whatever network the handheld joins next —
// a café, a hotel — so they stop themselves:
//
//   - after fileServerIdleLimit with no file operation: an FTP command other
//     than NOOP, an SFTP packet, or data moving on a transfer. Keepalives do
//     not count, since clients send those on their own while nobody is there;
//   - when the device's LAN address or Wi-Fi network differs from the one the
//     server started on, or the device has no address at all. A change has to
//     be seen on fileServerNetStrikes checks in a row, so a roaming blip
//     between two access points does not cut a transfer off.
//
// The watchdog is one goroutine, started with the first server and gone with
// the last. It never reads the config (the render thread owns that map); it
// asks the interfaces and wpa_cli (netsh on Windows), which is why it is not on
// the render thread either.

import (
	"net"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// fileServerIdleLimit is how long a server waits for a client to use it.
	fileServerIdleLimit = 15 * time.Minute
	// fileServerWatchInterval is how often the watchdog looks.
	fileServerWatchInterval = 15 * time.Second
	// fileServerNetStrikes is how many checks in a row must see another
	// network (or none) before the servers stop.
	fileServerNetStrikes = 2
)

// Why the watchdog stopped a server.
const (
	fsStopIdle    = "idle"
	fsStopOffline = "offline"
	fsStopNetwork = "network"
	// fsStopUnwatched: the watchdog itself failed.
	fsStopUnwatched = "unwatched"
)

// fsIdleLimitText is fileServerIdleLimit in words, e.g. "15 minutes".
func fsIdleLimitText() string {
	if m := int(fileServerIdleLimit / time.Minute); m != 1 {
		return strconv.Itoa(m) + " minutes"
	}
	return "1 minute"
}

// fileServerOnTrimui reports whether this is the handheld. A variable so the
// tests can say so on a computer.
var fileServerOnTrimui = func() bool { return IsTrimuiDevice() }

// fileServerCardRoot is the handheld's SD card, or "" on a computer. JukaHub
// lives in <card>/Apps/JukaHub, so on the handheld the executable's path names
// the card first; the fixed mount points cover a binary started from somewhere
// else. Only on the handheld: on a Linux computer ~/Apps/JukaHub would make
// the home folder "the card", and FTP would share ~/Documents and ~/Downloads.
func fileServerCardRoot() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if fileServerOnTrimui() {
		if root := strings.TrimSpace(trimuiSDRootFn()); root != "" && dirExists(root) {
			return root
		}
	}
	for _, candidate := range []string{"/mnt/SDCARD", "/mnt/sdcard", "/sdcard"} {
		if dirExists(candidate) {
			return candidate
		}
	}
	return ""
}

// ── Activity ──────────────────────────────────────────────────────────────

// fsActivity is when a server last did something for a client. It is touched
// on every command and every data buffer, so it is a lock-free timestamp.
//
// The stamp is on the monotonic clock, as time since fsActivityEpoch (plus
// one, so zero still means "never"). Network time (ntpsync.go) moves the wall
// clock: with a wall-clock stamp a forward set right after boot would stop
// the servers at once as idle, and a backward set would hold off the idle
// stop by as much.
type fsActivity struct{ last atomic.Int64 }

// fsActivityEpoch is the activity clock's zero. It carries a monotonic
// reading, and so does every time lastUsed derives from it.
var fsActivityEpoch = time.Now()

func (a *fsActivity) touch() {
	if a != nil {
		a.last.Store(int64(time.Since(fsActivityEpoch)) + 1)
	}
}

// lastUsed is the last touch, or the zero time before the first. Compare it
// only with a time.Now() of this run (the watchdog does): both then carry a
// monotonic reading, which Sub, Before and After use instead of the wall clock.
func (a *fsActivity) lastUsed() time.Time {
	if a == nil {
		return time.Time{}
	}
	if n := a.last.Load(); n != 0 {
		return fsActivityEpoch.Add(time.Duration(n - 1))
	}
	return time.Time{}
}

// ── Connected clients ─────────────────────────────────────────────────────

// fsClient is one connection, as the Network tools page lists it.
type fsClient struct {
	Remote   string
	Since    time.Time
	User     string
	Transfer string
}

// fsClients is a server's live connections.
type fsClients struct {
	mu   sync.Mutex
	next uint64
	byID map[uint64]*fsClient
}

// fsClientsVersion changes whenever either server's list does, so an open
// sheet knows to rebuild without comparing lists every frame.
var fsClientsVersion atomic.Uint64

// add registers a connection from remote ("ip:port") and returns its id.
func (t *fsClients) add(remote string) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = make(map[uint64]*fsClient)
	}
	t.next++
	t.byID[t.next] = &fsClient{Remote: remote, Since: time.Now()}
	fsClientsVersion.Add(1)
	return t.next
}

// remove forgets a connection; an id already cleared by Stop is ignored.
func (t *fsClients) remove(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.byID[id]; ok {
		delete(t.byID, id)
		fsClientsVersion.Add(1)
	}
}

// setUser records who signed in on a connection.
func (t *fsClients) setUser(id uint64, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.byID[id]; c != nil && c.User != user {
		c.User = user
		fsClientsVersion.Add(1)
	}
}

// setTransfer records what a connection is moving ("" when it is idle).
func (t *fsClients) setTransfer(id uint64, what string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.byID[id]; c != nil && c.Transfer != what {
		c.Transfer = what
		fsClientsVersion.Add(1)
	}
}

// clearTransfer empties the transfer only if it is still what — a client
// that opened a second file meanwhile keeps showing that one.
func (t *fsClients) clearTransfer(id uint64, what string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.byID[id]; c != nil && c.Transfer == what && what != "" {
		c.Transfer = ""
		fsClientsVersion.Add(1)
	}
}

// clear forgets every connection (the server stopped).
func (t *fsClients) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.byID) > 0 {
		t.byID = make(map[uint64]*fsClient)
		fsClientsVersion.Add(1)
	}
}

// list copies the connections, oldest first.
func (t *fsClients) list() []fsClient {
	t.mu.Lock()
	out := make([]fsClient, 0, len(t.byID))
	for _, c := range t.byID {
		out = append(out, *c)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Remote < out[j].Remote
	})
	return out
}

// count is how many connections are open.
func (t *fsClients) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byID)
}

// fsClientHost is the address part of "ip:port".
func fsClientHost(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil && host != "" {
		return host
	}
	return remote
}

// fsClientLines writes a server sheet's "Connected" block: a count, then one
// indented line per client (the sheet lays those out as continuation lines,
// which must not contain double spaces).
func fsClientLines(b *strings.Builder, clients []fsClient) {
	if len(clients) == 0 {
		b.WriteString("Connected: nobody\n")
		return
	}
	b.WriteString("Connected: " + netToolsCount(len(clients), "client", "clients") + "\n")
	for _, c := range clients {
		line := fsClientHost(c.Remote) + " · connected " + netToolsAge(c.Since)
		if c.User == "" {
			line += " · signing in"
		}
		if c.Transfer != "" {
			line += " · " + netToolsClip(c.Transfer, 48)
		}
		b.WriteString("    " + strings.Join(strings.Fields(line), " ") + "\n")
	}
}

// ── Network identity ──────────────────────────────────────────────────────

// fsNetIdentity is a network as the watchdog tells them apart.
type fsNetIdentity struct {
	ip   string
	ssid string
}

// fsReadNetIdentity reads the LAN address and, when Wi-Fi is up, its name. It
// runs wpa_cli (netsh on Windows), so only the watchdog calls it.
func fsReadNetIdentity() fsNetIdentity {
	id := fsNetIdentity{ip: localIPv4()}
	if id.ip == "" {
		return id
	}
	if st, err := wifiReadStatus(); err == nil && st.Connected {
		id.ssid = strings.TrimSpace(st.SSID)
	}
	return id
}

// fsNetChange says how cur differs from base: "" for the same network,
// fsStopOffline without an address, fsStopNetwork for another one. A network
// name that could not be read on either side is not a change: wpa_cli can time
// out, and a cable has no name.
func fsNetChange(base, cur fsNetIdentity) string {
	switch {
	case cur.ip == "":
		return fsStopOffline
	case cur.ip != base.ip:
		return fsStopNetwork
	case base.ssid != "" && cur.ssid != "" && base.ssid != cur.ssid:
		return fsStopNetwork
	}
	return ""
}

// ── Watchdog ──────────────────────────────────────────────────────────────

// fsWatchTarget is one server as the watchdog sees it on one check.
type fsWatchTarget struct {
	running  bool
	gen      uint64        // changes on every start
	base     fsNetIdentity // the network it started on (address only)
	lastUsed time.Time
}

// fsWatchSlot is what the watchdog remembers about one server's current run.
type fsWatchSlot struct {
	gen     uint64
	base    fsNetIdentity
	strikes int
}

// fsWatcher decides when servers stop. It holds no clock and reads no
// network: the loop feeds it both, and the tests feed it whatever they need.
type fsWatcher struct {
	idleLimit time.Duration
	slots     []fsWatchSlot
}

// decide returns, for each target, why it must stop now ("" keeps it). The
// loop passes time.Now(), whose monotonic reading the idle test compares with
// lastUsed's, so setting the wall clock moves neither.
func (w *fsWatcher) decide(now time.Time, cur fsNetIdentity, targets []fsWatchTarget) []string {
	out := make([]string, len(targets))
	for len(w.slots) < len(targets) {
		w.slots = append(w.slots, fsWatchSlot{})
	}
	for i, t := range targets {
		slot := &w.slots[i]
		if !t.running {
			*slot = fsWatchSlot{}
			continue
		}
		if slot.gen != t.gen {
			// A new run: its own network, no strikes carried over.
			*slot = fsWatchSlot{gen: t.gen, base: t.base}
		}
		// The start only records the address (reading the SSID runs a
		// program); the first check on that address learns the name.
		if slot.base.ssid == "" && cur.ip != "" && cur.ip == slot.base.ip {
			slot.base.ssid = cur.ssid
		}
		if change := fsNetChange(slot.base, cur); change != "" {
			slot.strikes++
			if slot.strikes >= fileServerNetStrikes {
				out[i] = change
			}
			continue
		}
		slot.strikes = 0
		if !t.lastUsed.IsZero() && now.Sub(t.lastUsed) >= w.idleLimit {
			out[i] = fsStopIdle
		}
	}
	return out
}

// fsStopMessage is the toast for servers the watchdog stopped for one reason.
func fsStopMessage(names []string, reason string) string {
	if len(names) == 0 {
		return ""
	}
	who := names[0] + " server stopped"
	them := "it"
	if len(names) > 1 {
		who = strings.Join(names, " and ") + " servers stopped"
		them = "them"
	}
	switch reason {
	case fsStopIdle:
		return who + " — nobody used " + them + " for " + fsIdleLimitText()
	case fsStopOffline:
		return who + " — the device is no longer on a network"
	case fsStopNetwork:
		return who + " — the device joined a different network"
	case fsStopUnwatched:
		return who + " — the check that switches " + them + " off failed; start " + them + " again from Network tools"
	}
	return who
}

// fsWatch is the watchdog goroutine's bookkeeping.
var fsWatch struct {
	mu      sync.Mutex
	running bool
}

// fileServerWatchStart makes sure the watchdog runs. Servers call it after a
// start; one goroutine watches both.
func fileServerWatchStart() {
	fsWatch.mu.Lock()
	defer fsWatch.mu.Unlock()
	if fsWatch.running {
		return
	}
	fsWatch.running = true
	go fileServerWatchLoop()
}

// fileServerWatchDone reports whether the watchdog can exit: no server runs.
// It decides under the watchdog's lock, so a server starting at that moment
// either sees the watchdog still running or starts a new one.
func fileServerWatchDone() bool {
	fsWatch.mu.Lock()
	defer fsWatch.mu.Unlock()
	if ftpSrv.Running() || sshSrv.Running() {
		return false
	}
	fsWatch.running = false
	return true
}

func fileServerWatchLoop() {
	defer func() {
		if r := recover(); r != nil {
			logError("fileserver", "watchdog stopped: %v", r)
			fsWatch.mu.Lock()
			fsWatch.running = false
			fsWatch.mu.Unlock()
			// A server nobody watches would outlive an idle day or a move to
			// a café's network: fail closed, and say so.
			fileServerStopUnwatched()
		}
	}()
	w := &fsWatcher{idleLimit: fileServerIdleLimit}
	ticker := time.NewTicker(fileServerWatchInterval)
	defer ticker.Stop()
	for range ticker.C {
		if fileServerWatchDone() {
			return
		}
		ftpTarget, sshTarget := ftpSrv.watchTarget(), sshSrv.watchTarget()
		cur := fsReadNetIdentity()
		// time.Now(), not a stored or parsed time: its monotonic reading is
		// what makes the idle test immune to clock sets (fsActivity).
		reasons := w.decide(time.Now(), cur, []fsWatchTarget{ftpTarget, sshTarget})
		fileServerApplyStops([]fsWatchTarget{ftpTarget, sshTarget}, reasons)
	}
}

// fileServerStopUnwatched stops whichever servers run after the watchdog
// failed. A start makes a new watchdog, so they can be started again.
func fileServerStopUnwatched() {
	var names []string
	if ftpSrv.Running() {
		ftpSrv.Stop()
		names = append(names, "FTP")
	}
	if sshSrv.Running() {
		sshSrv.Stop()
		names = append(names, "SFTP")
	}
	if len(names) > 0 {
		ShowToast(fsStopMessage(names, fsStopUnwatched), ToastKindWarning)
	}
}

// fileServerApplyStops stops the servers decide picked and says why in one
// toast; when they stopped for different reasons the network one is told,
// because it is the one the user did not cause.
func fileServerApplyStops(targets []fsWatchTarget, reasons []string) {
	names := []string{"FTP", "SFTP"}
	byReason := map[string][]string{}
	for i, reason := range reasons {
		if reason == "" || i >= len(targets) || i >= len(names) {
			continue
		}
		stopped := false
		switch i {
		case 0:
			stopped = ftpSrv.stopRun(targets[i].gen)
		case 1:
			stopped = sshSrv.stopRun(targets[i].gen)
		}
		if !stopped {
			continue
		}
		logInfo("fileserver", "%s server stopped: %s", names[i], reason)
		byReason[reason] = append(byReason[reason], names[i])
	}
	for _, reason := range []string{fsStopNetwork, fsStopOffline, fsStopIdle} {
		if list := byReason[reason]; len(list) > 0 {
			kind := ToastKindWarning
			if reason == fsStopIdle {
				kind = ToastKindInfo
			}
			ShowToast(fsStopMessage(list, reason), kind)
			return
		}
	}
}
