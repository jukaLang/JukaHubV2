package main

// wifi_ctrl_test.go — the supplicant socket transport, and the picker working
// on an image that ships no wpa_cli.
//
// The device that reported this has a radio and a running supplicant — it was
// downloading over Wi-Fi — but no wpa_cli, so wifiControlAvailable() answered
// false and the start menu dropped the Wi-Fi row entirely. These tests pin the
// transport that removes the binary dependency: the same commands, the same
// parsers, and the same "unavailable" answer when neither route exists.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCtrlConn answers reads from a queue and records what was written, so a
// test can see the command the supplicant would have received.
type fakeCtrlConn struct {
	replies []string
	writes  strings.Builder
	closed  bool
}

func (c *fakeCtrlConn) Read(p []byte) (int, error) {
	if len(c.replies) == 0 {
		return 0, io.EOF
	}
	reply := c.replies[0]
	c.replies = c.replies[1:]
	return copy(p, reply), nil
}

func (c *fakeCtrlConn) Write(p []byte) (int, error) { return c.writes.Write(p) }

func (c *fakeCtrlConn) Close() error {
	c.closed = true
	return nil
}

func withFakeCtrlConn(t *testing.T, conn *fakeCtrlConn) {
	t.Helper()
	prev := wpaCtrlOpen
	wpaCtrlOpen = func(string, time.Duration) (io.ReadWriteCloser, error) { return conn, nil }
	t.Cleanup(func() { wpaCtrlOpen = prev })
}

func TestWpaCtrlRequestWritesTheCommandAndReadsItsReply(t *testing.T) {
	conn := &fakeCtrlConn{replies: []string{"wpa_state=COMPLETED\n"}}
	withFakeCtrlConn(t, conn)

	reply, err := wpaCtrlRequest("/run/wpa_supplicant/wlan0", "status", time.Second)
	if err != nil {
		t.Fatalf("wpaCtrlRequest: %v", err)
	}
	if reply != "wpa_state=COMPLETED" {
		t.Fatalf("reply = %q, want the trimmed reply", reply)
	}
	if got := conn.writes.String(); got != "status\n" {
		t.Fatalf("wrote %q, want one command line", got)
	}
	if !conn.closed {
		t.Error("the socket was not closed")
	}
}

// The supplicant only writes events to attached clients, but one arriving while
// a command is in flight must not be returned as that command's answer.
func TestWpaCtrlRequestStepsOverEventMessages(t *testing.T) {
	conn := &fakeCtrlConn{replies: []string{
		"<3>CTRL-EVENT-SCAN-STARTED \n",
		"<3>CTRL-EVENT-SCAN-RESULTS \n",
		"OK\n",
	}}
	withFakeCtrlConn(t, conn)

	reply, err := wpaCtrlRequest("/run/wpa_supplicant/wlan0", "scan", time.Second)
	if err != nil {
		t.Fatalf("wpaCtrlRequest: %v", err)
	}
	if reply != "OK" {
		t.Fatalf("reply = %q, want the command's own answer", reply)
	}
}

func TestWpaCtrlRequestReportsASocketThatClosesWithoutAnswering(t *testing.T) {
	conn := &fakeCtrlConn{}
	withFakeCtrlConn(t, conn)

	_, err := wpaCtrlRequest("/run/wpa_supplicant/wlan0", "status", time.Second)
	if err == nil {
		t.Fatal("a socket that closed without answering was reported as a success")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("error = %v, want it to name the command that went unanswered", err)
	}
}

func TestWpaCtrlRequestRefusesAnEmptyCommand(t *testing.T) {
	withFakeCtrlConn(t, &fakeCtrlConn{})
	if _, err := wpaCtrlRequest("/run/wpa_supplicant/wlan0", "  \n", time.Second); err == nil {
		t.Fatal("an empty command was sent to the supplicant")
	}
}

// wifiSocketFixture presents the TG5040 shape: no radio in sysfs, no
// /proc/net/wireless entry, no wpa_cli, and one supplicant control socket whose
// name is the interface. Everything the backend needs is in the socket
// directory, which is how the Brick and the Smart Pro images are built.
func wifiSocketFixture(t *testing.T) (*fakeCtrlConn, string) {
	t.Helper()
	root := t.TempDir()
	prevRoot, prevProc, prevDirs := wifiSysfsNetRoot, wifiProcWirelessPath, wifiSupplicantDirs
	prevExists, prevRunner, prevOpen := wifiToolExists, wifiRunner, wpaCtrlOpen
	prevSocketExists := wpaCtrlSocketExists

	sockets := filepath.Join(root, "sockets")
	if err := os.MkdirAll(sockets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sockets, "wlan0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wifiSysfsNetRoot = root
	wifiProcWirelessPath = filepath.Join(root, "no-proc-wireless")
	wifiSupplicantDirs = []string{sockets}
	wifiToolExists = func(string) bool { return false }
	wifiRunner = func(_ time.Duration, _, name string, args ...string) (string, error) {
		return "", errors.New(name + " must not run: this image ships no wpa_cli")
	}
	conn := &fakeCtrlConn{}
	wpaCtrlOpen = func(path string, _ time.Duration) (io.ReadWriteCloser, error) {
		if filepath.Base(path) != "wlan0" {
			return nil, errors.New("dialled " + path + ", want the interface's own socket")
		}
		return conn, nil
	}
	t.Setenv("JUKAHUB_WIFI_IFACE", "")
	resetSupplicantInterfaceCache()

	t.Cleanup(func() {
		wifiSysfsNetRoot, wifiProcWirelessPath, wifiSupplicantDirs = prevRoot, prevProc, prevDirs
		wifiToolExists, wifiRunner, wpaCtrlOpen = prevExists, prevRunner, prevOpen
		wpaCtrlSocketExists = prevSocketExists
		resetSupplicantInterfaceCache()
	})
	return conn, sockets
}

func TestSocketBackendServesThePickerWithoutWpaCli(t *testing.T) {
	conn, _ := wifiSocketFixture(t)
	conn.replies = []string{"wpa_state=COMPLETED\nssid=Home\nip_address=192.168.1.5\n"}

	if got := wifiCommandBackend(); got != wifiBackendSocket {
		t.Fatalf("backend = %q, want the supplicant socket", got)
	}
	if !wifiControlAvailable() {
		t.Fatalf("Wi-Fi reported unavailable with a radio and a socket: %s", wifiUnavailableReason())
	}
	st, err := wifiReadStatus()
	if err != nil {
		t.Fatalf("wifiReadStatus over the socket: %v", err)
	}
	if !st.Connected || st.SSID != "Home" || st.IP != "192.168.1.5" {
		t.Fatalf("status = %+v, want the socket's answer parsed", st)
	}
	if got := conn.writes.String(); got != "status\n" {
		t.Fatalf("wrote %q, want the status command", got)
	}
}

// The passphrase still never reaches a process argument list: over the socket it
// is part of the command text, exactly as wpa_cli's stdin mode sends it, and no
// executable is started at all on an image without the CLI.
func TestSocketBackendSendsThePassphraseAsACommand(t *testing.T) {
	conn, _ := wifiSocketFixture(t)
	conn.replies = []string{"OK\n"}

	if _, err := wifiSetPassphrase(3, "correct horse battery"); err != nil {
		t.Fatalf("wifiSetPassphrase over the socket: %v", err)
	}
	got := conn.writes.String()
	if !strings.HasPrefix(got, `set_network 3 psk "correct horse battery"`) {
		t.Fatalf("wrote %q, want the quoted psk command", got)
	}
}

// A refused command is a printed FAIL, not a transport failure: the script runs
// to the end exactly as wpa_cli's stdin mode does, and the caller turns the FAIL
// into the sentence the panel shows.
func TestSocketScriptKeepsTheSuppliantsFailForTheCaller(t *testing.T) {
	conn, sockets := wifiSocketFixture(t)
	conn.replies = []string{"FAIL\n", "OK\n"}

	out, err := wifiSocketScript(sockets, "wlan0", "add_network\nselect_network 0\n")
	if err != nil {
		t.Fatalf("a printed FAIL was reported as a transport failure: %v", err)
	}
	if got := conn.writes.String(); got != "add_network\nselect_network 0\n" {
		t.Fatalf("wrote %q, want both commands of the script", got)
	}
	if err := wifiCommandError(out, nil); err == nil || !strings.Contains(err.Error(), "FAIL") {
		t.Fatalf("wifiCommandError(%q) = %v, want the FAIL named", out, err)
	}
}

func TestUnavailableReasonNamesBothRoutes(t *testing.T) {
	root := t.TempDir()
	prevRoot, prevProc, prevDirs := wifiSysfsNetRoot, wifiProcWirelessPath, wifiSupplicantDirs
	prevExists, prevRunner := wifiToolExists, wifiRunner
	wifiSysfsNetRoot = root
	wifiProcWirelessPath = filepath.Join(root, "no-proc-wireless")
	wifiSupplicantDirs = nil
	wifiToolExists = func(string) bool { return false }
	wifiRunner = func(_ time.Duration, _, name string, args ...string) (string, error) {
		return "", errors.New(name + " must not run")
	}
	t.Setenv("JUKAHUB_WIFI_IFACE", "wlan0")
	resetSupplicantInterfaceCache()
	t.Cleanup(func() {
		wifiSysfsNetRoot, wifiProcWirelessPath, wifiSupplicantDirs = prevRoot, prevProc, prevDirs
		wifiToolExists, wifiRunner = prevExists, prevRunner
		resetSupplicantInterfaceCache()
	})

	if got := wifiCommandBackend(); got != "" {
		t.Fatalf("backend = %q with neither a CLI nor a socket", got)
	}
	if wifiControlAvailable() {
		t.Fatal("Wi-Fi reported available with neither route")
	}
	reason := wifiUnavailableReason()
	if !strings.Contains(reason, "wpa_cli") || !strings.Contains(reason, "socket") {
		t.Fatalf("reason = %q, want it to name both routes", reason)
	}
}

// A socket directory with no socket for the interface is not a backend: the
// supplicant may simply not be running yet.
func TestSocketDirectoryWithoutTheInterfaceSocketIsNotABackend(t *testing.T) {
	_, sockets := wifiSocketFixture(t)
	t.Setenv("JUKAHUB_WIFI_IFACE", "wlan0")
	if err := os.Remove(filepath.Join(sockets, "wlan0")); err != nil {
		t.Fatal(err)
	}
	resetSupplicantInterfaceCache()

	if got := wifiInterface(); got != "wlan0" {
		t.Fatalf("interface = %q, want the override so the socket is the only open question", got)
	}
	if got := wifiCommandBackend(); got != "" {
		t.Fatalf("backend = %q with an empty socket directory", got)
	}
}
