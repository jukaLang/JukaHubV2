package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPartyBaseURLLegitimateShapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"192.168.1.5", "http://192.168.1.5:8318"},
		{"192.168.1.5:9318", "http://192.168.1.5:9318"},
		{"10.0.0.2:8318", "http://10.0.0.2:8318"},
		{"172.16.0.5:8318", "http://172.16.0.5:8318"},
		{"localhost", "http://localhost:8318"},
		{"localhost:9318", "http://localhost:9318"},
		{"127.0.0.1:8318", "http://127.0.0.1:8318"},
		{"[::1]:8318", "http://[::1]:8318"},
		{"169.254.10.20:8318", "http://169.254.10.20:8318"},
		{"http://169.254.10.20:8318", "http://169.254.10.20:8318"},
		{"[fe80::1]:8318", "http://[fe80::1]:8318"},
		{"http://[fe80::1%25eth0]:8318", "http://[fe80::1%25eth0]:8318"},
		{"http://127.0.0.1:8318", "http://127.0.0.1:8318"},
		{"http://[::1]:8318", "http://[::1]:8318"},
		{"myhandheld.local:8318", "http://myhandheld.local:8318"},
		{"my-handheld.local:8318", "http://my-handheld.local:8318"},
		{"handheld:8318", "http://handheld:8318"},
		{"http://192.168.1.5:8318/", "http://192.168.1.5:8318"},
	}
	for _, tc := range cases {
		got, err := partyNormalizeBase(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("partyNormalizeBase(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestSafeAbsolutePathRejectsTraversalButKeepsDottedNames(t *testing.T) {
	root := t.TempDir()
	if got, err := safeAbsolutePath(filepath.Join(root, "state")); err != nil || got == "" {
		t.Fatalf("safe absolute path rejected: %q %v", got, err)
	}
	if got, err := safeAbsolutePath("relative/path"); err != nil || got != filepath.Clean("relative/path") {
		t.Fatalf("ordinary relative path rejected: %q %v", got, err)
	}
	if _, err := safeAbsolutePath(filepath.Join(root, "save..state")); err != nil {
		t.Fatalf("a legal dotted filename was rejected: %v", err)
	}
	if _, err := safeAbsolutePath(root + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside"); err == nil {
		t.Fatal("traversal path was accepted")
	}
}

func TestPartyBaseURLHostileInputs(t *testing.T) {
	cases := []string{
		"example.com:8318",
		"8.8.8.8:8318",
		"169.254.169.254",
		"169.254.169.254:80",
		"http://169.254.169.254/latest/meta-data",
		"169.254.170.2:80",
		"100.100.100.200:80",
		"http://100.100.100.200/latest/meta-data",
		"http://[fd00:ec2::254]/latest/meta-data",
		"http://user:pass@192.168.1.5:8318",
		"https://192.168.1.5:8318",
		"ftp://192.168.1.5:8318",
		"http://192.168.1.5:8318/api/party/../etc",
		"http://192.168.1.5:8318/%2e%2e/etc",
	}
	for _, in := range cases {
		if got, err := partyNormalizeBase(in); err == nil {
			t.Errorf("partyNormalizeBase(%q) = %q, want an error", in, got)
		}
	}
}

func TestPartyInviteAndRequestShapes(t *testing.T) {
	invites := []struct {
		in   string
		want string
	}{
		{"192.168.1.5:8318 ABCD2", "http://192.168.1.5:8318"},
		{"169.254.10.20:8318 ABCD2", "http://169.254.10.20:8318"},
		{"[fe80::1%25eth0]:8318 ABCD2", "http://[fe80::1%25eth0]:8318"},
		{"localhost:8318 ABCD2", "http://localhost:8318"},
		{"[::1]:8318 ABCD2", "http://[::1]:8318"},
		{"myhandheld.local ABCD2", "http://myhandheld.local:8318"},
		{"handheld ABCD2", "http://handheld:8318"},
		{"http://127.0.0.1:8318/ ABCD2", "http://127.0.0.1:8318"},
	}
	for _, tc := range invites {
		base, code, err := partyParseInvite(tc.in)
		if err != nil || base != tc.want || code != "ABCD2" {
			t.Errorf("partyParseInvite(%q) = %q, %q, %v; want %q, ABCD2", tc.in, base, code, err, tc.want)
		}
	}
	for _, in := range []string{
		"http://user:pass@192.168.1.5:8318 ABCD2",
		"169.254.169.254:8318 ABCD2",
		"100.100.100.200:8318 ABCD2",
		"[fd00:ec2::254]:8318 ABCD2",
		"example.com:8318 ABCD2",
	} {
		if _, _, err := partyParseInvite(in); err == nil {
			t.Errorf("partyParseInvite(%q) accepted a hostile invite", in)
		}
	}

	for _, in := range []string{
		"http://192.168.1.5:8318/api/party/state?code=ABCD2&member=x",
		"http://localhost:8318/api/party/queue",
		"http://[::1]:8318/api/party/join",
		"http://handheld.local:8318/api/party/state",
		"http://127.0.0.1:8318/api/party/state",
		"http://169.254.10.20:8318/api/party/state",
		"http://[fe80::1%25eth0]:8318/api/party/state",
	} {
		if _, err := partySafeRequestURL(in); err != nil {
			t.Errorf("partySafeRequestURL(%q) rejected a legitimate endpoint: %v", in, err)
		}
	}
	for _, in := range []string{
		"http://example.com/api/party/state",
		"http://169.254.169.254/api/party/state",
		"http://169.254.170.2/api/party/state",
		"http://100.100.100.200/api/party/state",
		"http://[fd00:ec2::254]/api/party/state",
		"http://user:pass@192.168.1.5:8318/api/party/state",
		"http://192.168.1.5:8318/api/party/../state",
		"https://192.168.1.5:8318/api/party/state",
		"http://192.168.1.5:8318/other",
	} {
		if _, err := partySafeRequestURL(in); err == nil {
			t.Errorf("partySafeRequestURL(%q) accepted a hostile endpoint", in)
		}
	}
}

func TestStateAndResetHelpersAcceptRealPaths(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		gmetaStaged.mu.Lock()
		gmetaStaged.path = ""
		gmetaStaged.mu.Unlock()
	})
	rom := filepath.Join(root, "Game.gba")
	state := filepath.Join(root, "Game.state")
	if err := os.WriteFile(rom, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}

	states := gmetaListStates(rom, []string{root})
	if len(states) != 1 || states[0].Path != state {
		t.Fatalf("gmetaListStates = %#v; want the state in %q", states, state)
	}

	copyPath := filepath.Join(root, "Game.copy.state")
	if err := gmetaCopyFile(state, copyPath); err != nil {
		t.Fatalf("gmetaCopyFile: %v", err)
	}
	if got, _ := os.ReadFile(copyPath); string(got) != "state" {
		t.Fatalf("copied state = %q", got)
	}

	a, b := filepath.Join(root, "a.state"), filepath.Join(root, "b.state")
	if err := os.WriteFile(a, []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gmetaSwapFiles(a, b); err != nil {
		t.Fatalf("gmetaSwapFiles: %v", err)
	}
	if gotA, _ := os.ReadFile(a); string(gotA) != "B" {
		t.Fatalf("a after swap = %q; want B", gotA)
	}
	if gotB, _ := os.ReadFile(b); string(gotB) != "A" {
		t.Fatalf("b after swap = %q; want A", gotB)
	}

	auto := filepath.Join(root, "Game.state.auto")
	if err := os.WriteFile(auto, []byte("auto"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gmetaKeepPrevAuto(auto); err != nil {
		t.Fatalf("gmetaKeepPrevAuto: %v", err)
	}
	if got, _ := os.ReadFile(auto + gmetaPrevSuffix); string(got) != "auto" {
		t.Fatalf("previous auto = %q; want auto", got)
	}
	if err := os.WriteFile(auto+gmetaPrevSuffix, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gmetaSwapPrevAuto(auto + gmetaPrevSuffix); err != nil {
		t.Fatalf("gmetaSwapPrevAuto: %v", err)
	}
	if got, _ := os.ReadFile(auto); string(got) != "previous" {
		t.Fatalf("auto after swapping the previous save = %q; want previous", got)
	}
	if got, _ := os.ReadFile(auto + gmetaPrevSuffix); string(got) != "auto" {
		t.Fatalf("previous after swapping = %q; want auto", got)
	}

	gmetaStaged.mu.Lock()
	gmetaStaged.path = ""
	gmetaStaged.mu.Unlock()
	if err := gmetaStageState(gmetaState{Path: state, Slot: 0, Time: time.Now()}); err != nil {
		t.Fatalf("gmetaStageState: %v", err)
	}
	if got, _ := os.ReadFile(auto); string(got) != "state" {
		t.Fatalf("staged auto = %q; want state", got)
	}

	thumb := filepath.Join(root, "thumb.png")
	pic := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	out, err := os.Create(thumb)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(out, pic); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gmetaLoadThumb(context.Background(), thumb); err != nil {
		t.Fatalf("gmetaLoadThumb rejected a real thumbnail path: %v", err)
	}

	src := filepath.Join(root, "save.sav")
	dst := filepath.Join(root, "reset-trash", "save.sav")
	if err := os.WriteFile(src, []byte("save"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resetTrashMoveTo(src, dst); err != nil {
		t.Fatalf("resetTrashMoveTo: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists after reset move: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "save" {
		t.Fatalf("trashed save = %q; want save", got)
	}

	for _, bad := range []string{
		root + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside.state",
		"../outside.state",
	} {
		if _, err := resetTrashMoveTo(bad, filepath.Join(root, "trash", "bad.state")); err == nil {
			t.Errorf("resetTrashMoveTo accepted hostile source %q", bad)
		}
	}
}

func TestNetplayBeaconReadsRealGamePath(t *testing.T) {
	rom := filepath.Join(t.TempDir(), "Game.gba")
	if err := os.WriteFile(rom, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := netplayBeaconFor(netplayPlan{
		Session: netplaySession{Game: libraryGame{Path: rom, System: "GBA", Title: "Game"}, Port: 8318},
		Core:    "mgba_libretro.so",
	})
	if info.Size != 5 {
		t.Fatalf("beacon size = %d; want 5", info.Size)
	}
}

func TestAppLaunchPathGuardAcceptsRealAppNames(t *testing.T) {
	for _, path := range []string{
		`C:\Apps\Game (USA).exe`,
		`/mnt/SDCARD/Apps/My Game.sh`,
		`C:\ProgramData\Microsoft\Windows\Start Menu\Programs\Tools & Games.lnk`,
		`C:\Apps\100%.exe`,
	} {
		if !appLaunchPathRE.MatchString(path) {
			t.Errorf("appLaunchPathRE rejected real app path %q", path)
		}
	}
	for _, path := range []string{
		"line\nbreak.exe",
		"nul\x00byte.exe",
	} {
		if appLaunchPathRE.MatchString(path) {
			t.Errorf("appLaunchPathRE accepted unsafe app path %q", path)
		}
	}
}

func TestScriptPathValidationAcceptsRealFile(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "launch.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filesNav.location
	filesNav.location = ""
	t.Cleanup(func() { filesNav.location = old })
	config := &Config{Variables: Variables{FileExplorerRoot: root}}
	got, err := feScriptFile(config, script)
	if err != nil || !strings.EqualFold(got, script) {
		t.Fatalf("feScriptFile(%q) = %q, %v; want the script", script, got, err)
	}
	if _, err := feScriptFile(config, filepath.Join(root, "not-a-script.txt")); err == nil {
		t.Fatal("feScriptFile accepted a non-.sh file")
	}
	outside := filepath.Join(t.TempDir(), "outside.sh")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := feScriptFile(config, outside); err == nil {
		t.Fatal("feScriptFile accepted a script outside the root")
	}
}

func TestTerminalShellRunsTypedCommandFromStdin(t *testing.T) {
	cmd := termShellCommand(context.Background(), "echo terminal-stdin-ok")
	if cmd == nil || cmd.Stdin == nil {
		t.Fatal("termShellCommand did not provide the stdin script pipe")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("termShellCommand: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "terminal-stdin-ok") {
		t.Fatalf("shell output = %q; typed command was not run", out)
	}
}

// SEC-14: a terminal_run / emulator_run command that hangs is stopped at its
// timeout, the pipeline it started with it, and says so; the output keeps
// its last 4 000 bytes.
func TestTerminalRunCommandStopsAHangingCommand(t *testing.T) {
	hang := "echo started; sleep 30 | cat"
	if IsWindows() {
		hang = "echo started& ping -n 30 127.0.0.1 >nul"
	}
	begin := time.Now()
	out := runTerminalCommand(hang, 500*time.Millisecond)
	if took := time.Since(begin); took > 10*time.Second {
		t.Fatalf("a hanging command held the run for %v", took)
	}
	if !strings.Contains(out, "started") || !strings.Contains(out, "(stopped after 500ms)") {
		t.Errorf("output = %q, want the command's first line and the stop note", out)
	}
	if out := runTerminalCommand("echo terminal-run-ok", 10*time.Second); !strings.Contains(out, "terminal-run-ok") || strings.Contains(out, "stopped after") {
		t.Errorf("a quick command printed %q", out)
	}
	long := "echo " + strings.Repeat("x", 5000)
	if out := runTerminalCommand(long, 10*time.Second); len(out) > terminalRunMaxOutput || !strings.Contains(out, "xxx") {
		t.Errorf("long output kept %d bytes, want at most %d", len(out), terminalRunMaxOutput)
	}
	// Output is kept as a running tail (never all of it in memory): the end
	// of 20 KB of lines arrives, the start does not.
	if !IsWindows() {
		out := runTerminalCommand(`i=0; while [ $i -lt 2000 ]; do i=$((i+1)); echo line-$i; done`, 10*time.Second)
		if !strings.HasSuffix(strings.TrimSpace(out), "line-2000") || strings.Contains(out, "line-100\n") || len(out) > terminalRunMaxOutput {
			t.Errorf("20 KB of output kept %d bytes ending %q, want the last lines only", len(out), out[max(0, len(out)-20):])
		}
	}
}

func TestIntegerConversionsStayInRange(t *testing.T) {
	if got := brightnessRawAtomic(-1); got != 0 {
		t.Errorf("brightnessRawAtomic(-1) = %d; want 0", got)
	}
	if got := brightnessRawAtomic(42); got != 42 {
		t.Errorf("brightnessRawAtomic(42) = %d; want 42", got)
	}
	if got := bzParseRSSI("0xffffffc4"); got != -60 {
		t.Errorf("bzParseRSSI(0xffffffc4) = %d; want -60", got)
	}
	if got := bzParseRSSI("0x7fffffff"); got != math.MaxInt32 {
		t.Errorf("bzParseRSSI(0x7fffffff) = %d; want %d", got, math.MaxInt32)
	}
	if _, ok := apkxSDKValue(1 << 40); ok {
		t.Error("apkxSDKValue accepted an out-of-range SDK")
	}
	if _, ok := apkxSDKValue(-1); ok {
		t.Error("apkxSDKValue accepted a negative SDK")
	}
	if got, ok := apkxSDKValue(21); !ok || got != 21 {
		t.Errorf("apkxSDKValue(21) = %d, %v", got, ok)
	}
	if _, ok := apkxGLESValue(1 << 40); ok {
		t.Error("apkxGLESValue accepted an out-of-range version")
	}
	if _, ok := apkxGLESValue(-1); ok {
		t.Error("apkxGLESValue accepted a negative version")
	}
	if got, ok := apkxGLESValue(0x00030000); !ok || got != 0x00030000 {
		t.Errorf("apkxGLESValue(0x30000) = %#x, %v", got, ok)
	}
}

// TestPartyRequestsReachLoopbackServerAndBlockHostileTargets drives the real
// join, post and Poll request paths. The test server listens on 127.0.0.1 and
// a transport dials that address for the RFC1918 and link-local host names, so
// the URL and host guard see production shapes while the request stays local.
func TestPartyRequestsReachLoopbackServerAndBlockHostileTargets(t *testing.T) {
	var (
		mu       sync.Mutex
		hits     int
		requests []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var snap partySnapshot
		switch r.URL.Path {
		case "/api/party/join":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"member_id": "member-1",
				"snapshot":  partySnapshot{Code: "ABCD2", Name: "Loopback Room", HostID: "host-1", Version: 3, ServerTime: 30},
			})
		case "/api/party/queue":
			snap = partySnapshot{Code: "ABCD2", Name: "Loopback Room", Version: 5, ServerTime: 50}
		case "/api/party/state":
			snap = partySnapshot{Code: "ABCD2", Name: "Loopback Room", Version: 6, ServerTime: 60}
		case "/api/party/leave":
			snap = partySnapshot{Code: "ABCD2", Version: 7, ServerTime: 70}
		default:
			http.NotFound(w, r)
			return
		}
		if snap.Code != "" {
			_ = json.NewEncoder(w).Encode(snap)
		}
	}))
	defer server.Close()
	serverAddr := server.Listener.Addr().String()
	port := server.Listener.Addr().(*net.TCPAddr).Port

	oldFactory := partyHTTPFactory
	partyHTTPFactory = func() *http.Client {
		return &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					var dialer net.Dialer
					return dialer.DialContext(ctx, network, serverAddr)
				},
			},
		}
	}
	t.Cleanup(func() {
		partyLeaveSession()
		partyHTTPFactory = oldFactory
	})

	base := func(host string) string {
		return fmt.Sprintf("http://%s:%d", host, port)
	}
	hostile := []string{
		base("169.254.169.254"),
		base("169.254.170.2"),
		base("100.100.100.200"),
		base("[fd00:ec2::254]"),
		base("example.com"),
		"http://user:pass@" + fmt.Sprintf("192.168.1.5:%d", port),
		"https://" + fmt.Sprintf("192.168.1.5:%d", port),
		base("192.168.1.5") + "/api/party/../state",
	}
	for _, target := range hostile {
		if err := partyJoin(target+" ABCD2", "Hostile"); err == nil {
			t.Errorf("partyJoin(%q) accepted a hostile target", target)
		}
	}
	for _, target := range hostile {
		client := newPartyClient(target, "ABCD2", "member-1")
		if _, err := client.post("/api/party/queue", map[string]string{"code": "ABCD2"}); err == nil {
			t.Errorf("post(%q) accepted a hostile target", target)
		}
		client.Poll(1)
		if client.Err() == "" {
			t.Errorf("Poll(%q) accepted a hostile target", target)
		}
	}
	mu.Lock()
	if hits != 0 {
		t.Fatalf("hostile targets reached the HTTP server: %d requests %v", hits, requests)
	}
	mu.Unlock()

	for _, target := range []string{
		base("192.168.1.5"),
		base("169.254.10.20"),
		base("[fe80::1%25eth0]"),
	} {
		if err := partyJoin(target+" ABCD2", "Tester"); err != nil {
			t.Fatalf("partyJoin(%q): %v", target, err)
		}
		snap, _, ok, lastErr := partyState()
		if !ok || snap.Code != "ABCD2" || snap.Version != 3 || lastErr != "" {
			t.Fatalf("partyJoin(%q) state = %#v, ok=%v, err=%q", target, snap, ok, lastErr)
		}
	}

	for _, target := range []string{base("192.168.1.5"), base("169.254.10.20")} {
		client := newPartyClient(target, "ABCD2", "member-1")
		snap, err := client.post("/api/party/queue", map[string]string{"code": "ABCD2", "member": "member-1"})
		if err != nil || snap.Code != "ABCD2" || snap.Version != 5 {
			t.Fatalf("post through %q = %#v, %v", target, snap, err)
		}
		if !client.Poll(1.25) {
			t.Fatalf("Poll through %q did not start", target)
		}
		if snap, _, ok := client.Snapshot(); !ok || snap.Code != "ABCD2" || snap.Version != 6 {
			t.Fatalf("Poll through %q = %#v, ok=%v", target, snap, ok)
		}
	}

	mu.Lock()
	got := append([]string(nil), requests...)
	mu.Unlock()
	joined, posted, polled := 0, 0, 0
	for _, request := range got {
		switch {
		case strings.HasPrefix(request, "POST /api/party/join"):
			joined++
		case strings.HasPrefix(request, "POST /api/party/queue"):
			posted++
		case strings.HasPrefix(request, "GET /api/party/state"):
			polled++
		}
	}
	if joined < 3 || posted != 2 || polled != 2 {
		t.Fatalf("loopback request log = %v; want 3 joins, 2 posts, 2 polls", got)
	}
}
