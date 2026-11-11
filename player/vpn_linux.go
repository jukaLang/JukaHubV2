//go:build linux

package main

// vpn_linux.go — the VPN's platform half on Linux (the handheld): starting,
// watching and stopping the tunnel process (vpnd_linux.go), which is this
// same binary run as `JukaHub --vpn-daemon`.
//
// The daemon is started in a session of its own, so the firmware's stop
// signals to JukaHub's process group do not take the tunnel down in the
// middle of a screen handoff, and it is anchored to JukaHub's parent — on the
// handheld launch.sh, which runs for the whole JukaHub session, PS Remote
// Play's screen handoff and update restarts included. A JukaHub that starts
// again finds the daemon still running and adopts it when it runs the same
// payload (the hash in its status, which also covers vpnDaemonVersion, so a
// daemon left by an older build is replaced).

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// vpnStale is how long a running daemon may go without rewriting its status
// before it counts as hung and is replaced.
const vpnStale = 20 * time.Second

// vpnSeen is the last status sequence the worker saw and when it changed.
// Only the worker goroutine touches it (vpnPlatformProbe).
var vpnSeen struct {
	pid int
	seq uint64
	at  time.Time
}

// handleVPNCommand runs the tunnel process when JukaHub was started as one.
func handleVPNCommand(args []string) (handled bool, exitCode int) {
	for _, a := range args {
		if a == "--vpn-daemon" || a == "-vpn-daemon" {
			return true, vpndMain(args)
		}
	}
	return false, 0
}

// vpnRunDir holds the daemon's status and lock, in RAM.
func vpnRunDir() string { return filepath.Join(os.TempDir(), "jukahub-vpn") }

// vpnDaemonStateDir is where the daemon keeps what must survive a power cut
// (the original DNS settings, the handshake clock): the data folder, or the
// run folder when there is none.
func vpnDaemonStateDir() string {
	vpnMgr.mu.Lock()
	dir := vpnMgr.dataDir
	vpnMgr.mu.Unlock()
	if dir == "" {
		return vpnRunDir()
	}
	return dir
}

func vpnPlatformSupport() (bool, string) {
	if os.Geteuid() != 0 {
		return false, "The VPN needs JukaHub to run as root (the handheld's firmware does that)"
	}
	return true, ""
}

func vpnPlatformAuto() bool { return true }

func vpnReadDaemonStatus() (vpnDaemonStatus, bool) {
	var st vpnDaemonStatus
	data, err := os.ReadFile(filepath.Join(vpnRunDir(), vpndStatusFile))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, true
}

// vpnDaemonAlive reports whether pid is a tunnel process (not a pid some
// other program reused).
func vpnDaemonAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && bytes.Contains(append(cmdline, 0), []byte("\x00--vpn-daemon\x00"))
}

// vpnFindDaemon finds a tunnel process by its command line, for when the
// status file is gone.
func vpnFindDaemon() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		if vpnDaemonAlive(pid) {
			return pid
		}
	}
	return 0
}

func vpnPlatformProbe() vpnProbe {
	st, ok := vpnReadDaemonStatus()
	if !ok {
		return vpnProbe{}
	}
	alive := vpnDaemonAlive(st.PID)
	p := vpnProbe{
		Instance:  strconv.Itoa(st.PID),
		Hash:      st.Hash,
		Detail:    st.Error,
		Warning:   st.Warning,
		Fatal:     st.Fatal,
		Endpoint:  st.Endpoint,
		Addresses: st.Addresses,
		DNS:       st.DNS,
		Rx:        st.Rx,
		Tx:        st.Tx,
	}
	for _, e := range st.Events {
		p.Events = append(p.Events, vpnEvent{Seq: e.Seq, Text: e.Text, Warn: e.Warn})
	}
	if st.HandshakeAgo >= 0 {
		p.Handshake, p.HasHandshake = time.Duration(st.HandshakeAgo)*time.Second, true
	}
	now := time.Now()
	if st.PID != vpnSeen.pid || st.Seq != vpnSeen.seq {
		vpnSeen.pid, vpnSeen.seq, vpnSeen.at = st.PID, st.Seq, now
	}
	switch {
	case st.State == "error":
		p.State = vpnStateError
		p.Running = alive // a failed daemon is on its way out
	case !alive && st.State != "stopped":
		p.State, p.Detail = vpnStateError, "the tunnel process stopped unexpectedly"
	case !alive:
		p.State = vpnStateOff
	case now.Sub(vpnSeen.at) > vpnStale:
		// Alive but silent: running nothing anyone asked for (no hash), so
		// the worker replaces it while the VPN is on (vpnPlatformApply stops
		// it first) and stops it when the VPN is off.
		p.Running, p.Hash = true, ""
		p.State, p.Detail = vpnStateError, "the tunnel process stopped answering; starting it again"
	case st.State == "starting" || st.State == "waiting":
		p.Running, p.State = true, vpnStateConnecting
	case p.HasHandshake && p.Handshake < wgRejectAfterTime:
		p.Running, p.State = true, vpnStateConnected
	case st.GaveUp || st.Unanswered >= 15:
		p.Running, p.State = true, vpnStateNoAnswer
	case p.HasHandshake:
		// The session aged out while idle; the next packet makes a new one.
		p.Running, p.State = true, vpnStateConnected
	default:
		p.Running, p.State = true, vpnStateConnecting
	}
	return p
}

// vpnPlatformApply runs payload: whatever tunnel runs is stopped first.
func vpnPlatformApply(payload []byte) error {
	if err := vpnPlatformStop(); err != nil {
		return err
	}
	dir := vpnRunDir()
	if err := vpndPrepareRunDir(dir); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, vpndStatusFile))
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// The anchor is the session: launch.sh on the handheld. Started some
	// other way (by init, or a helper that has exited), JukaHub itself is.
	anchor := os.Getppid()
	if anchor <= 1 {
		anchor = os.Getpid()
	}
	cmd := exec.Command(exe, "--vpn-daemon", "--vpn-run-dir", dir, "--vpn-state-dir", vpnDaemonStateDir(),
		"--vpn-anchor", strconv.Itoa(anchor))
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// vpnPlatformStop ends the tunnel process: SIGTERM, then SIGKILL after 5 s,
// and puts back what a killed one could not.
func vpnPlatformStop() error {
	st, _ := vpnReadDaemonStatus()
	pid := st.PID
	if !vpnDaemonAlive(pid) {
		pid = vpnFindDaemon()
	}
	if pid != 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 50 && vpnDaemonAlive(pid); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if vpnDaemonAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			time.Sleep(300 * time.Millisecond)
		}
	}
	vpnPlatformRecover()
	return nil
}

// vpnPlatformRecover puts back the DNS settings and removes the unreachable
// routes of a tunnel that died without doing it, when no tunnel runs.
func vpnPlatformRecover() {
	if st, ok := vpnReadDaemonStatus(); ok && vpnDaemonAlive(st.PID) {
		return
	}
	if vpnFindDaemon() != 0 {
		return
	}
	if vpndRecoverDNS(vpnDaemonStateDir()) {
		logInfo("vpn", "put back the DNS settings a stopped tunnel left behind")
	}
	vpndRecoverRoutes(vpnRunDir())
}
