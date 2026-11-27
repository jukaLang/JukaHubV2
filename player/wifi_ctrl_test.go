package main

import (
	"io"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeSupplicant answers wpaCtrlRequest's reads in order and keeps what was
// written, so the transport's decisions (event messages stepped over, the
// command in wire form) are testable on every platform.
type fakeSupplicant struct {
	reads  []string
	writes []string
	closed bool
}

func (f *fakeSupplicant) Read(p []byte) (int, error) {
	if len(f.reads) == 0 {
		return 0, io.EOF
	}
	s := f.reads[0]
	f.reads = f.reads[1:]
	return copy(p, s), nil
}

func (f *fakeSupplicant) Write(p []byte) (int, error) {
	f.writes = append(f.writes, string(p))
	return len(p), nil
}

func (f *fakeSupplicant) Close() error {
	f.closed = true
	return nil
}

// withSupplicant points the socket transport at a stub for one test.
func withSupplicant(t *testing.T, stub *fakeSupplicant) {
	t.Helper()
	old := wpaCtrlOpen
	wpaCtrlOpen = func(string, time.Duration) (io.ReadWriteCloser, error) { return stub, nil }
	t.Cleanup(func() { wpaCtrlOpen = old })
}

// The reply to a command is the first message that is not an unsolicited
// event; the command goes out in wpa_cli's wire form.
func TestSupplicantRequestReadsPastEvents(t *testing.T) {
	stub := &fakeSupplicant{reads: []string{"<3>CTRL-EVENT-SCAN-STARTED\n", "wpa_state=COMPLETED\n"}}
	withSupplicant(t, stub)
	reply, err := wpaCtrlRequest("/tmp/wlan0", "status", time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if reply != "wpa_state=COMPLETED" {
		t.Errorf("reply = %q, want the answer after the event", reply)
	}
	if len(stub.writes) != 1 || stub.writes[0] != "STATUS" {
		t.Errorf("wrote %v, want one upper-cased command", stub.writes)
	}
	if !stub.closed {
		t.Error("the client socket was not closed")
	}
}

// An answer that never comes must not read as an empty success.
func TestSupplicantRequestWithoutAnswerIsAnError(t *testing.T) {
	stub := &fakeSupplicant{reads: []string{"<3>CTRL-EVENT-SCAN-STARTED\n"}}
	withSupplicant(t, stub)
	if _, err := wpaCtrlRequest("/tmp/wlan0", "status", time.Second); err == nil {
		t.Fatal("a closed socket with only events was accepted as an answer")
	}
	// A refused command still travels: the caller keeps script order.
	stub = &fakeSupplicant{reads: []string{"FAIL\n"}}
	withSupplicant(t, stub)
	if got, err := wpaCtrlRequest("/tmp/wlan0", "save_config", time.Second); err != nil || got != "FAIL" {
		t.Fatalf("FAIL reply = %q, %v; want it returned as the answer", got, err)
	}
}

// wpa_supplicant's control interface is one UNIX datagram out and one back. A
// build without wpa_cli depends on this transport for every Wi-Fi action, so it
// is also tested against a real stub supplicant where the OS has datagrams.
func TestSupplicantSocketTransport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unixgram sockets are not available for this test on Windows")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "wlan0")
	server, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()

	sent := make(chan string, 1)
	go func() {
		buf := make([]byte, wpaCtrlMaxReply)
		n, addr, err := server.ReadFromUnix(buf)
		if err != nil {
			return
		}
		sent <- string(buf[:n])
		// An unsolicited event first: only clients that attach receive them,
		// but a build that did would read one as the answer.
		_, _ = server.WriteToUnix([]byte("<3>CTRL-EVENT-SCAN-STARTED\n"), addr)
		_, _ = server.WriteToUnix([]byte("wpa_state=COMPLETED\n"), addr)
	}()

	reply, err := wpaCtrlRequest(sock, "status", 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if reply != "wpa_state=COMPLETED" {
		t.Errorf("reply = %q, want the answer after the event", reply)
	}
	select {
	case got := <-sent:
		if got != "STATUS" {
			t.Errorf("sent %q, want the upper-cased verb and no line ending", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stub supplicant never received a command")
	}
}

// A script sends one command per line, in order, and joins the replies in the
// order they came: callers detect a FAIL in the joined text.
func TestSupplicantSocketScriptJoinsReplies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unixgram sockets are not available for this test on Windows")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "wlan0")
	server, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()

	commands := make(chan []string, 1)
	go func() {
		var seen []string
		buf := make([]byte, wpaCtrlMaxReply)
		for i := 0; i < 3; i++ {
			n, addr, err := server.ReadFromUnix(buf)
			if err != nil {
				return
			}
			cmd := string(buf[:n])
			seen = append(seen, cmd)
			answer := "OK\n" + cmd
			if cmd == "SAVE_CONFIG" {
				answer = "FAIL"
			}
			_, _ = server.WriteToUnix([]byte(answer), addr)
		}
		commands <- seen
	}()

	got, err := wifiSocketScript(dir, "wlan0", "set_network 0 ssid \"x\"\nsave_config", "enable_network 0")
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	select {
	case seen := <-commands:
		want := []string{"SET_NETWORK 0 ssid \"x\"", "SAVE_CONFIG", "ENABLE_NETWORK 0"}
		if len(seen) != len(want) {
			t.Fatalf("sent %v, want %v", seen, want)
		}
		for i := range want {
			if seen[i] != want[i] {
				t.Errorf("command %d = %q, want %q", i, seen[i], want[i])
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stub supplicant never received the script")
	}
	// The FAIL is part of the joined reply; the transport does not turn it
	// into an error (the caller's wifiCommandError reads it).
	if want := "OK\nSET_NETWORK 0 ssid \"x\"\nFAIL\nOK\nENABLE_NETWORK 0"; got != want {
		t.Errorf("replies = %q, want %q", got, want)
	}
}

// A verb with no arguments is sent alone; arguments keep their spelling.
func TestSupplicantWireCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"status", "STATUS"},
		{"  scan_results  ", "SCAN_RESULTS"},
		{"set_network 0 psk \"a b\"", "SET_NETWORK 0 psk \"a b\""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := wpaCtrlWireCommand(tc.in); got != tc.want {
			t.Errorf("wpaCtrlWireCommand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := wpaCtrlRequest("/tmp/wlan0", "  ", time.Second); err == nil {
		t.Error("an empty command was sent")
	}
}
