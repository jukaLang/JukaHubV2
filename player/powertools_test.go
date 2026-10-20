package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	// Saves performed by the rest of the test suite must not create a
	// history folder in the checkout; the snapshot helpers are still tested
	// directly below.
	powerConfigHistoryEnabled = false
}

func TestPowerGatewayGuess(t *testing.T) {
	cases := map[string]string{
		"192.168.1.57": "192.168.1.1",
		"10.0.0.4":     "10.0.0.1",
		"127.0.0.1":    "",
		"":             "",
		"not-an-ip":    "",
	}
	for in, want := range cases {
		if got := powerGatewayGuess(in); got != want {
			t.Errorf("powerGatewayGuess(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPowerWirelessParse(t *testing.T) {
	text := `Inter-| sta-|   Quality        |   Discarded packets
 face | tus | link level noise |  nwid  crypt   frag  retry   misc
 wlan0: 0000   70.  -45.  -256        0      0      0      0      0        0
`
	link, level, ok := parsePowerWireless(text)
	if !ok || link != "70" || level != "-45" {
		t.Fatalf("parse = %q, %q, %v; want 70, -45, true", link, level, ok)
	}
	if _, _, ok := parsePowerWireless("no interface here"); ok {
		t.Fatal("header-only file reported an associated interface")
	}
}

func TestPowerHexDump(t *testing.T) {
	out := powerHexDump([]byte("hello"), 5)
	if !strings.Contains(out, "00000000") || !strings.Contains(out, "68 65 6c 6c 6f") {
		t.Fatalf("hex dump missing offset/bytes: %q", out)
	}
	if !strings.Contains(out, "|hello|") {
		t.Fatalf("hex dump missing ASCII column: %q", out)
	}
	trunc := powerHexDump(make([]byte, 64), 32)
	if !strings.Contains(trunc, "32 more bytes") {
		t.Fatalf("truncated dump did not say how much was hidden: %q", trunc)
	}
}

func TestPowerDiffLines(t *testing.T) {
	got := powerDiffLines("a\nb\nc", "a\nB\nc", 20)
	if !strings.Contains(got, "- b") || !strings.Contains(got, "+ B") {
		t.Fatalf("diff = %q, want - b and + B", got)
	}
	if strings.Contains(got, "- a") || strings.Contains(got, "+ c") {
		t.Fatalf("diff included unchanged lines: %q", got)
	}
	if got := powerDiffLines("same", "same", 20); got != "No changes." {
		t.Fatalf("identical diff = %q", got)
	}
}

func TestPowerJSONInspection(t *testing.T) {
	data := []byte(`{"zeta":1,"alpha":2}`)
	pretty, err := powerPrettyJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pretty), "\n  \"zeta\"") {
		t.Fatalf("pretty JSON not indented: %q", pretty)
	}
	text, mode := powerPreviewBytes("x.json", data)
	if mode != "json" || !strings.Contains(text, "alpha") {
		t.Fatalf("preview = %q mode=%q", text, mode)
	}
	if kind := powerFileKind("errors.txt", []byte("line\n")); kind != "log" {
		t.Fatalf("errors.txt kind = %q, want log", kind)
	}
	if kind := powerFileKind("blob.bin", []byte{0, 1, 2}); kind != "binary" {
		t.Fatalf("binary kind = %q", kind)
	}
}

func TestPowerCleanHost(t *testing.T) {
	cases := map[string]string{
		"192.168.1.5":      "192.168.1.5",
		"nas.local:8317":   "nas.local",
		"  example.com  ":  "example.com",
		"http://1.2.3.4":   "",
		"192.168.1.5/path": "",
		"":                 "",
	}
	for in, want := range cases {
		got, err := powerCleanHost(in)
		if want == "" {
			if err == nil {
				t.Errorf("powerCleanHost(%q) = %q, want an error", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("powerCleanHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestPowerPortScanFindsListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	open := powerScanPorts("127.0.0.1", []int{port}, time.Second)
	if len(open) != 1 || open[0] != port {
		t.Fatalf("scan = %v, want [%d]", open, port)
	}
	if got := powerScanPorts("", []int{port}, time.Second); got != nil {
		t.Fatalf("empty host scan = %v, want nil", got)
	}
}

func TestPowerResolveLocalhost(t *testing.T) {
	ips, err := powerResolve("localhost")
	if err != nil {
		t.Skipf("resolver unavailable: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("localhost resolved to no addresses")
	}
}

func TestPowerRunScript(t *testing.T) {
	out, err := runPowerScript("echo power-tools-ok", 5*time.Second)
	if err != nil {
		t.Fatalf("script failed: %v (%q)", err, out)
	}
	if !strings.Contains(out, "power-tools-ok") {
		t.Fatalf("output = %q", out)
	}
}

func TestPowerScriptLibraryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "power-scripts.json")
	list := []powerScript{{Name: "One", Command: "echo 1"}, {Name: "", Command: "x"}}
	if err := savePowerScriptsTo(path, list); err != nil {
		t.Fatal(err)
	}
	back, err := loadPowerScriptsFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Name != "One" {
		t.Fatalf("round trip = %+v, want only the complete entry", back)
	}
}

func TestPowerConfigHistoryAndRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukauser.json")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotPowerConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := snapshotPowerConfig(path); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := powerListSnapshots(path); len(snaps) != 1 {
		t.Fatalf("identical snapshot was stored twice: %d", len(snaps))
	}
	time.Sleep(5 * time.Millisecond)
	if err := os.WriteFile(path, []byte("{\"a\":2}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotPowerConfig(path); err != nil {
		t.Fatal(err)
	}
	snaps, err := powerListSnapshots(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}
	current, _ := os.ReadFile(path)
	old, _ := os.ReadFile(snaps[1].Path)
	diff := powerDiffLines(string(old), string(current), 20)
	if !strings.Contains(diff, "- {\"a\":1}") || !strings.Contains(diff, "+ {\"a\":2}") {
		t.Fatalf("diff = %q", diff)
	}
	if err := powerRestoreSnapshot(path, snaps[1].Path); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != "{\"a\":1}\n" {
		t.Fatalf("restored = %q", restored)
	}
}

func TestPowerSnapshotRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jukauser.json")
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := powerRestoreSnapshot(path, bad); err == nil {
		t.Fatal("restored a snapshot that is not valid JSON")
	}
}

func TestPowerBenchStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "power-bench.json")
	for _, e := range []powerBenchEntry{
		{Device: "slow", Write: 5, Read: 4},
		{Device: "fast", Write: 80, Read: 70},
	} {
		if err := recordPowerBenchmarkTo(path, e); err != nil {
			t.Fatal(err)
		}
	}
	list, err := loadPowerBenchFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Device != "fast" {
		t.Fatalf("board = %+v, want fast first and invalid entries dropped", list)
	}
	if list[0].At == "" || list[0].Kind != "sequential" {
		t.Fatalf("entry defaults missing: %+v", list[0])
	}
	if err := recordPowerBenchmarkTo(path, powerBenchEntry{Device: "zero"}); err == nil {
		t.Fatal("accepted an incomplete benchmark entry")
	}
}

func TestPowerRsyncArgs(t *testing.T) {
	src := t.TempDir()
	args, err := powerRsyncArgs(src, "user@host:/backup", true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dry-run") || !strings.Contains(joined, "--itemize-changes") {
		t.Fatalf("args = %v", args)
	}
	if strings.Contains(joined, "--delete") {
		t.Fatalf("rsync args must never delete: %v", args)
	}
	if _, err := powerRsyncArgs(src, "-oops", true); err == nil {
		t.Fatal("accepted a target that looks like an option")
	}
	if _, err := powerRsyncArgs(src, "host:$(reboot)", true); err == nil {
		t.Fatal("accepted a remote target with shell metacharacters")
	}
	if _, err := powerRsyncArgs(src, "", true); err == nil {
		t.Fatal("accepted an empty target")
	}
}
