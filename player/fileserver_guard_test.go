package main

// Tests for what the FTP and SFTP servers share (fileserver_guard.go): the
// watchdog's idle and network decisions on an injected clock, the client list,
// and SFTP's default root.

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A server stops after fileServerIdleLimit without use; use restarts the wait,
// and a stopped server is never "stopped" again.
func TestFileServerWatcherStopsWhenIdle(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	home := fsNetIdentity{ip: "192.168.1.20", ssid: "Home"}
	w := &fsWatcher{idleLimit: fileServerIdleLimit}
	server := fsWatchTarget{running: true, gen: 1, base: fsNetIdentity{ip: home.ip}, lastUsed: start}

	if got := w.decide(start.Add(fileServerIdleLimit-time.Second), home, []fsWatchTarget{server}); got[0] != "" {
		t.Fatalf("stopped before the idle limit: %q", got[0])
	}
	if got := w.decide(start.Add(fileServerIdleLimit), home, []fsWatchTarget{server}); got[0] != fsStopIdle {
		t.Fatalf("at the idle limit: %q, want %q", got[0], fsStopIdle)
	}
	server.lastUsed = start.Add(10 * time.Minute)
	if got := w.decide(start.Add(20*time.Minute), home, []fsWatchTarget{server}); got[0] != "" {
		t.Fatalf("stopped 10 minutes after the last use: %q", got[0])
	}
	stopped := fsWatchTarget{gen: 1, base: server.base, lastUsed: start}
	if got := w.decide(start.Add(time.Hour), home, []fsWatchTarget{stopped}); got[0] != "" {
		t.Fatalf("a stopped server was given a reason: %q", got[0])
	}
}

// The idle clock runs on the monotonic clock: lastUsed carries a monotonic
// reading, so the watchdog's time.Now() is compared with it by that reading
// and a network-time set of the wall clock neither stops a server at once nor
// holds off its idle stop.
func TestFsActivityIsMonotonic(t *testing.T) {
	var a fsActivity
	if !a.lastUsed().IsZero() {
		t.Fatal("a server nobody used has a last use")
	}
	a.touch()
	used := a.lastUsed()
	if !strings.Contains(used.String(), " m=") {
		t.Fatalf("lastUsed %v carries no monotonic reading", used)
	}
	if d := time.Since(used); d < 0 || d > time.Minute {
		t.Fatalf("time since the touch = %v", d)
	}
	home := fsNetIdentity{ip: "192.168.1.20"}
	w := &fsWatcher{idleLimit: fileServerIdleLimit}
	server := fsWatchTarget{running: true, gen: 1, base: home, lastUsed: used}
	if got := w.decide(time.Now(), home, []fsWatchTarget{server}); got[0] != "" {
		t.Fatalf("stopped right after use: %q", got[0])
	}
	time.Sleep(20 * time.Millisecond) // more than a Windows clock tick
	a.touch()
	if later := a.lastUsed(); !later.After(used) {
		t.Fatalf("a later touch %v is not after %v", later, used)
	}
}

// A different network (address or Wi-Fi name) or none stops the server, but
// only when two checks in a row see it: one blip is not enough. A restart on
// the new network starts over from that network.
func TestFileServerWatcherStopsOnNetworkChange(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	home := fsNetIdentity{ip: "192.168.1.20", ssid: "Home"}
	cafe := fsNetIdentity{ip: "10.0.0.7", ssid: "Cafe"}
	sameIPOtherSSID := fsNetIdentity{ip: home.ip, ssid: "Neighbour"}
	offline := fsNetIdentity{}
	server := fsWatchTarget{running: true, gen: 1, base: fsNetIdentity{ip: home.ip}, lastUsed: now}

	check := func(w *fsWatcher, cur fsNetIdentity, target fsWatchTarget) string {
		t.Helper()
		return w.decide(now, cur, []fsWatchTarget{target})[0]
	}

	w := &fsWatcher{idleLimit: fileServerIdleLimit}
	if got := check(w, home, server); got != "" {
		t.Fatalf("same network: %q", got)
	}
	// The first check learnt the Wi-Fi name; another name on the same address
	// is another network.
	if got := check(w, sameIPOtherSSID, server); got != "" {
		t.Fatalf("one check on another network already stopped it: %q", got)
	}
	if got := check(w, sameIPOtherSSID, server); got != fsStopNetwork {
		t.Fatalf("two checks on another Wi-Fi: %q, want %q", got, fsStopNetwork)
	}

	w = &fsWatcher{idleLimit: fileServerIdleLimit}
	check(w, home, server)
	if got := check(w, offline, server); got != "" {
		t.Fatalf("one check offline already stopped it: %q", got)
	}
	if got := check(w, home, server); got != "" {
		t.Fatalf("back on the network after a blip: %q", got)
	}
	if got := check(w, offline, server); got != "" {
		t.Fatalf("the blip's strike was not forgotten: %q", got)
	}
	if got := check(w, offline, server); got != fsStopOffline {
		t.Fatalf("two checks offline: %q, want %q", got, fsStopOffline)
	}

	w = &fsWatcher{idleLimit: fileServerIdleLimit}
	check(w, cafe, server)
	if got := check(w, cafe, server); got != fsStopNetwork {
		t.Fatalf("two checks on another address: %q, want %q", got, fsStopNetwork)
	}
	restarted := fsWatchTarget{running: true, gen: 2, base: fsNetIdentity{ip: cafe.ip}, lastUsed: now}
	if got := check(w, cafe, restarted); got != "" {
		t.Fatalf("a server restarted on the new network was stopped: %q", got)
	}

	// A name that could not be read is not a change.
	w = &fsWatcher{idleLimit: fileServerIdleLimit}
	check(w, home, server)
	unread := fsNetIdentity{ip: home.ip}
	check(w, unread, server)
	if got := check(w, unread, server); got != "" {
		t.Fatalf("an unread Wi-Fi name stopped the server: %q", got)
	}
}

func TestFsNetChange(t *testing.T) {
	base := fsNetIdentity{ip: "192.168.1.20", ssid: "Home"}
	cases := []struct {
		cur  fsNetIdentity
		want string
	}{
		{fsNetIdentity{ip: "192.168.1.20", ssid: "Home"}, ""},
		{fsNetIdentity{ip: "192.168.1.20"}, ""},
		{fsNetIdentity{ip: "192.168.1.20", ssid: "Other"}, fsStopNetwork},
		{fsNetIdentity{ip: "192.168.1.21", ssid: "Home"}, fsStopNetwork},
		{fsNetIdentity{}, fsStopOffline},
	}
	for _, tc := range cases {
		if got := fsNetChange(base, tc.cur); got != tc.want {
			t.Errorf("fsNetChange(%+v) = %q, want %q", tc.cur, got, tc.want)
		}
	}
}

func TestFsStopMessage(t *testing.T) {
	if got := fsStopMessage([]string{"FTP"}, fsStopIdle); got != "FTP server stopped — nobody used it for 15 minutes" {
		t.Errorf("idle FTP = %q", got)
	}
	if got := fsStopMessage([]string{"FTP", "SFTP"}, fsStopNetwork); !strings.HasPrefix(got, "FTP and SFTP servers stopped") ||
		!strings.Contains(got, "different network") {
		t.Errorf("both on another network = %q", got)
	}
	if got := fsStopMessage(nil, fsStopIdle); got != "" {
		t.Errorf("no server = %q, want nothing", got)
	}
}

// The client list keeps who signed in and what each is moving; a transfer is
// only cleared by the one that set it.
func TestFsClientsTrackTransfers(t *testing.T) {
	var clients fsClients
	v0 := fsClientsVersion.Load()
	a := clients.add("192.168.1.30:50111")
	b := clients.add("192.168.1.31:50222")
	clients.setUser(a, "juka")
	clients.setTransfer(a, "uploading Roms/GBA/game.gba")
	clients.clearTransfer(a, "downloading something else")
	list := clients.list()
	if len(list) != 2 || list[0].User != "juka" || list[0].Transfer != "uploading Roms/GBA/game.gba" {
		t.Fatalf("clients = %+v", list)
	}
	if fsClientsVersion.Load() == v0 {
		t.Fatal("the version did not change")
	}
	var text strings.Builder
	fsClientLines(&text, list)
	out := text.String()
	if !strings.Contains(out, "Connected: 2 clients") || !strings.Contains(out, "192.168.1.30 ·") ||
		strings.Contains(out, ":50111") || !strings.Contains(out, "uploading Roms/GBA/game.gba") ||
		!strings.Contains(out, "signing in") {
		t.Fatalf("client lines:\n%s", out)
	}
	clients.clearTransfer(a, "uploading Roms/GBA/game.gba")
	clients.remove(b)
	if list := clients.list(); len(list) != 1 || list[0].Transfer != "" {
		t.Fatalf("after clear and remove: %+v", list)
	}
	clients.clear()
	if clients.count() != 0 {
		t.Fatal("clear left clients behind")
	}
	text.Reset()
	fsClientLines(&text, nil)
	if text.String() != "Connected: nobody\n" {
		t.Fatalf("empty list = %q", text.String())
	}
}

// SFTP shares the card, not "/", unless the advanced switch is on — and the
// executable's folder names the card only on the handheld: on a Linux
// computer ~/Apps/JukaHub must not make the home folder "the card".
func TestSSHRootDefaultsToTheCard(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the card and the single \"/\" are the handheld's (Linux) layout")
	}
	card := t.TempDir()
	prev, prevTrimui := trimuiSDRootFn, fileServerOnTrimui
	trimuiSDRootFn = func() string { return card }
	onTrimui := true
	fileServerOnTrimui = func() bool { return onTrimui }
	t.Cleanup(func() { trimuiSDRootFn, fileServerOnTrimui = prev, prevTrimui })
	t.Setenv("JUKAHUB_SSH_ROOT", "")
	t.Setenv("JUKAHUB_SSH_FULL_ROOT", "")
	t.Cleanup(func() { sshWholeSystemSession.Store(false) })

	if got := sshRootDir(); got != card {
		t.Fatalf("sshRootDir() = %q, want the card %q", got, card)
	}
	onTrimui = false
	if got := fileServerCardRoot(); got == card {
		t.Fatalf("on a computer the executable's folder %q was taken for the card", got)
	}
	onTrimui = true
	sshWholeSystemSession.Store(true)
	if got := sshRootDir(); got != "/" || !sshWholeSystemNext() {
		t.Fatalf("with the advanced switch: %q, want /", got)
	}
	sshWholeSystemSession.Store(false)
	t.Setenv("JUKAHUB_SSH_FULL_ROOT", "1")
	if got := sshRootDir(); got != "/" {
		t.Fatalf("with JUKAHUB_SSH_FULL_ROOT=1: %q, want /", got)
	}
	t.Setenv("JUKAHUB_SSH_ROOT", card)
	if got := sshRootDir(); got != card {
		t.Fatalf("JUKAHUB_SSH_ROOT must win: %q", got)
	}
	// The sheet warns about "/" only when the next start shares it.
	if sshWholeSystemNext() {
		t.Fatal("JUKAHUB_SSH_ROOT names a folder, yet the next start counts as the whole system")
	}
	t.Setenv("JUKAHUB_SSH_ROOT", "/")
	if !sshWholeSystemNext() {
		t.Fatal("JUKAHUB_SSH_ROOT=/ is the whole system")
	}
}

// On Windows a reserved device name (NUL, CON, COM1…) inside the SFTP root
// opens a device, not a file: the path mapping refuses it.
func TestSFTPRootRefusesWindowsDeviceNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("device names are a Windows thing")
	}
	r, err := newSFTPRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/NUL", "/games/con", "/COM1", "/a/AUX"} {
		if _, err := r.localPath(p); err == nil {
			t.Errorf("localPath(%q) was allowed", p)
		}
	}
	if _, err := r.localPath("/games/console.txt"); err != nil {
		t.Errorf("an ordinary name was refused: %v", err)
	}
}

// The idle stop's wording follows the constant.
func TestFsIdleLimitText(t *testing.T) {
	want := strconv.Itoa(int(fileServerIdleLimit/time.Minute)) + " minute"
	if got := fsIdleLimitText(); !strings.HasPrefix(got, want) {
		t.Fatalf("fsIdleLimitText() = %q, want %q…", got, want)
	}
	if got := fsStopMessage([]string{"FTP", "SFTP"}, fsStopUnwatched); !strings.Contains(got, "start them again") {
		t.Fatalf("watchdog failure message = %q", got)
	}
}
