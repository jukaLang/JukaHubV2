package main

// wifi_interface_test.go — how the radio is *found*, which is the question the
// picker answered wrongly on a handheld that was online.
//
// The probe only accepted entries of /sys/class/net that DirEntry.IsDir called a
// directory. On a real kernel every entry there is a symlink into the device
// tree, so the target is a directory but the link is not, and the answer was
// always "No wireless network interface on this device". The old fixture built
// real directories, which is exactly the shape that hides the bug.
//
// These tests build the tree the way a kernel presents it and cover the three
// other places a radio can be named, because some vendor drivers create neither
// a "wireless" nor a "phy80211" attribute.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wifiIfaceFixture points the probe at a temporary sysfs root and a proc path
// that does not exist, so the host's own network never leaks into a result. The
// supplicant probe is stubbed off and its cache cleared: on a developer machine
// that has wpa_cli and a radio, the fallback source would otherwise answer for
// the host instead of the fixture.
func wifiIfaceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prevRoot, prevProc, prevDirs := wifiSysfsNetRoot, wifiProcWirelessPath, wifiSupplicantDirs
	prevExists, prevRunner := wifiToolExists, wifiRunner
	wifiSysfsNetRoot = root
	wifiProcWirelessPath = filepath.Join(root, "proc-wireless")
	wifiSupplicantDirs = nil
	wifiToolExists = func(string) bool { return false }
	wifiRunner = func(_ time.Duration, _, name string, args ...string) (string, error) {
		return "", fmt.Errorf("%s %v must not run: this fixture has no supplicant", name, args)
	}
	resetSupplicantInterfaceCache()
	t.Cleanup(func() {
		wifiSysfsNetRoot, wifiProcWirelessPath, wifiSupplicantDirs = prevRoot, prevProc, prevDirs
		wifiToolExists, wifiRunner = prevExists, prevRunner
		resetSupplicantInterfaceCache()
	})
	return root
}

// One radio, presented the way /sys/class/net presents it: a symlink to the
// device. This is the regression the handheld hit.
func TestWirelessInterfaceFoundThroughASysfsSymlink(t *testing.T) {
	root := wifiIfaceFixture(t)
	device := filepath.Join(root, "devices", "platform", "mmc0", "wlan0")
	if err := os.MkdirAll(filepath.Join(device, "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, filepath.Join(root, "wlan0")); err != nil {
		t.Skipf("symlink: %v", err)
	}

	if got := wifiInterface(); got != "wlan0" {
		t.Fatalf("wifiInterface = %q, want wlan0 through the symlink", got)
	}
	// Whatever the host does or does not have installed, the picker must no
	// longer claim the radio is missing.
	if reason := wifiUnavailableReason(); strings.Contains(reason, "No wireless network interface") {
		t.Fatalf("the picker still reports %q with a radio present", reason)
	}

	// The status meter reads the same list: a symlinked interface that is up
	// must count as a live link, marker or not.
	if err := os.WriteFile(filepath.Join(device, "operstate"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !wifiLinkIsUp() {
		t.Fatal("an interface that is up through a symlink read as no link")
	}
}

// A vendor driver that registers a netdev without creating a wireless
// attribute: /proc/net/wireless still names it, and that must win over the
// naming convention for another interface.
func TestWirelessInterfaceFromProcWhenSysfsHasNoMarker(t *testing.T) {
	root := wifiIfaceFixture(t)
	// A conventional name that would be picked by the last-resort rule...
	if err := os.MkdirAll(filepath.Join(root, "ath9k0"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ...and the kernel's own list naming a different radio, which is evidence.
	proc := "Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE\n" +
		" face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22\n" +
		"   ra1: 0000   31.  -72.  -256        0      0      0      0      0        0\n" +
		"   ra0: 0000   47.  -63.  -256        0      0      0      0      0        0\n"
	if err := os.WriteFile(wifiProcWirelessPath, []byte(proc), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := wifiInterface(); got != "ra0" {
		t.Fatalf("wifiInterface = %q, want ra0 from /proc/net/wireless", got)
	}
}

// No marker and no proc file, but wpa_supplicant is managing a radio: its
// control socket names the interface.
func TestWirelessInterfaceFromSupplicantControlSocket(t *testing.T) {
	root := wifiIfaceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "wlan0"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctrl := filepath.Join(t.TempDir(), "wpa_supplicant")
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		t.Fatal(err)
	}
	// The socket, and the config file that shares the directory and must not be
	// mistaken for an interface.
	if err := os.WriteFile(filepath.Join(ctrl, "wlx00c0ca123456"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctrl, "wpa_supplicant.conf"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	wifiSupplicantDirs = []string{ctrl}

	if got := wifiInterface(); got != "wlx00c0ca123456" {
		t.Fatalf("wifiInterface = %q, want the supplicant's interface", got)
	}
}

// The naming convention is the last resort, and it must not invent a radio on a
// device that only has Ethernet.
func TestWirelessInterfaceNameConventionIsTheLastResort(t *testing.T) {
	root := wifiIfaceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "eth0", "operstate"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := wifiInterface(); got != "" {
		t.Fatalf("an Ethernet-only tree reported %q", got)
	}
	if !strings.Contains(wifiUnavailableReason(), "No wireless network interface") {
		t.Fatalf("unavailable reason = %q", wifiUnavailableReason())
	}
	if wifiLinkIsUp() {
		t.Fatal("an Ethernet link lit the Wi-Fi meter")
	}

	if err := os.MkdirAll(filepath.Join(root, "wlan0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := wifiInterface(); got != "wlan0" {
		t.Fatalf("wifiInterface = %q, want the wlan0 convention", got)
	}
}

// The parsers are pinned on their own so a formatting change in a fixture cannot
// hide a broken header filter.
func TestParseWirelessProcIfaces(t *testing.T) {
	proc := "Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE\n" +
		" face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22\n" +
		" wlan1: 0000   70.  -30.  -256        0      0      0      0      0        0\n" +
		" wlan0: 0000   52.  -48.  -256        0      0      0      0      0        0\n"
	got := parseWirelessProcIfaces(proc)
	if len(got) != 2 || got[0] != "wlan0" || got[1] != "wlan1" {
		t.Fatalf("parseWirelessProcIfaces = %v, want [wlan0 wlan1]", got)
	}
	if names := parseWirelessProcIfaces("Inter-| sta-|\n face | tus |\n"); len(names) != 0 {
		t.Fatalf("a header-only file produced %v", names)
	}
}

// ── The supplicant as a source ──────────────────────────────────────────────
//
// The first three sources read the card. Some images expose a radio in none of
// them — a vendor driver with no marker, a kernel without /proc/net/wireless and
// a ctrl_interface outside every known directory — and there the tool that owns
// the radio is the only thing that knows its name.

// stubSupplicant makes wpa_cli present and answers its calls through reply.
func stubSupplicant(t *testing.T, reply func(args []string) (string, error)) *int {
	t.Helper()
	calls := 0
	wifiToolExists = func(name string) bool { return name == "wpa_cli" }
	wifiRunner = func(_ time.Duration, _, _ string, args ...string) (string, error) {
		calls++
		return reply(args)
	}
	return &calls
}

// A netdev named nothing the convention recognises, on an image where sysfs,
// /proc and the control directory all came up empty.
func TestWirelessInterfaceFromSupplicantWhenStaticSourcesAreEmpty(t *testing.T) {
	root := wifiIfaceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "sdi0"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := stubSupplicant(t, func(args []string) (string, error) {
		if len(args) == 1 && args[0] == "interface" {
			return "IFNAME=sdi0\n", nil
		}
		return "", fmt.Errorf("unexpected wpa_cli call %v", args)
	})
	resetSupplicantInterfaceCache()

	if got := wifiInterface(); got != "sdi0" {
		t.Fatalf("wifiInterface = %q, want the supplicant's sdi0", got)
	}
	if *calls != 1 {
		t.Fatalf("the listing took %d calls, want exactly one", *calls)
	}
}

// An older wpa_cli answers the listing with nothing usable, so the probe asks
// each netdev for its status and takes the one the supplicant answers for.
func TestSupplicantProbeSweepsNetdevsWhenTheListingIsUnusable(t *testing.T) {
	root := wifiIfaceFixture(t)
	for _, name := range []string{"eth0", "lo", "sdi0"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	stubSupplicant(t, func(args []string) (string, error) {
		if len(args) == 1 && args[0] == "interface" {
			return "FAIL\n", nil
		}
		if len(args) == 3 && args[0] == "-i" {
			asked = append(asked, args[1])
			if args[1] == "sdi0" {
				return "ssid=Home\nwpa_state=COMPLETED\n", nil
			}
			return "FAIL\n", nil
		}
		return "", fmt.Errorf("unexpected wpa_cli call %v", args)
	})
	resetSupplicantInterfaceCache()

	if got := wifiInterface(); got != "sdi0" {
		t.Fatalf("wifiInterface = %q, want sdi0 from the status sweep", got)
	}
	for _, name := range asked {
		if name == "lo" {
			t.Fatal("the sweep asked loopback for its status")
		}
	}
	if len(asked) != 2 || asked[0] != "eth0" || asked[1] != "sdi0" {
		t.Fatalf("sweep asked %v, want [eth0 sdi0] and to stop at the answer", asked)
	}
}

// The sweep is bounded: discovery runs under the status bar, so a machine with
// many netdevs must not pay for all of them.
func TestSupplicantProbeSweepIsBounded(t *testing.T) {
	root := wifiIfaceFixture(t)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("nd%d", i)
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	asked := 0
	stubSupplicant(t, func(args []string) (string, error) {
		if len(args) == 3 && args[0] == "-i" {
			asked++
		}
		return "FAIL\n", nil
	})
	resetSupplicantInterfaceCache()

	if got := wifiInterface(); got != "" {
		t.Fatalf("wifiInterface = %q, want no answer when the supplicant holds none", got)
	}
	if asked != wifiProbeMaxCandidates {
		t.Fatalf("the sweep asked %d netdevs, want the bound of %d", asked, wifiProbeMaxCandidates)
	}
}

// A radio sysfs already names must cost nothing: the probe does not run at all.
func TestSupplicantProbeIsSkippedWhenSysfsNamesTheRadio(t *testing.T) {
	root := wifiIfaceFixture(t)
	device := filepath.Join(root, "devices", "wlan0")
	if err := os.MkdirAll(filepath.Join(device, "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, filepath.Join(root, "wlan0")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	calls := stubSupplicant(t, func(args []string) (string, error) {
		return "", fmt.Errorf("the probe ran %v for a radio sysfs already named", args)
	})
	resetSupplicantInterfaceCache()

	if got := wifiInterface(); got != "wlan0" {
		t.Fatalf("wifiInterface = %q, want wlan0", got)
	}
	if *calls != 0 {
		t.Fatalf("the supplicant probe ran %d times with a marked radio present", *calls)
	}
}

// The whole point of the cache: a per-frame reader must not fork wpa_cli every
// frame. 120 frames cost one probe; the answer is refreshed once its TTL passes.
func TestSupplicantInterfaceProbeIsCachedBetweenFrames(t *testing.T) {
	root := wifiIfaceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "sdi0"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := time.Unix(1700000000, 0)
	prevNow := wifiDrivenNow
	wifiDrivenNow = func() time.Time { return fake }
	t.Cleanup(func() { wifiDrivenNow = prevNow })
	calls := stubSupplicant(t, func(args []string) (string, error) {
		return "IFNAME=sdi0\n", nil
	})
	resetSupplicantInterfaceCache()

	for frame := 0; frame < 120; frame++ {
		if got := wifiInterface(); got != "sdi0" {
			t.Fatalf("frame %d: wifiInterface = %q", frame, got)
		}
	}
	if *calls != 1 {
		t.Fatalf("the probe ran %d times across 120 frames, want once", *calls)
	}

	fake = fake.Add(wifiDrivenTTL + time.Second)
	if got := wifiInterface(); got != "sdi0" {
		t.Fatalf("wifiInterface after the TTL = %q", got)
	}
	if *calls != 2 {
		t.Fatalf("the probe ran %d times after the TTL, want the answer refreshed once (2)", *calls)
	}
}

// Both shapes wpa_cli can answer with are read, and nothing that is not an
// interface name is taken for one.
func TestParseWpaCliInterfaceList(t *testing.T) {
	cases := []struct {
		out  string
		want []string
	}{
		{"IFNAME=wlan1\nIFNAME=wlan0\n", []string{"wlan0", "wlan1"}},
		{"Available interfaces are: wlan0\n", []string{"wlan0"}},
		{"Available interfaces are: wlan0 wlan1\n", []string{"wlan0", "wlan1"}},
		{"Selected interface 'wlx00c0ca123456'\n", []string{"wlx00c0ca123456"}},
		{"Available interfaces are: \n", nil},
		{"FAIL\n", nil},
		{"IFNAME=-bad\nIFNAME=ok0\n", []string{"ok0"}},
	}
	for _, tc := range cases {
		got := parseWpaCliInterfaceList(tc.out)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("parseWpaCliInterfaceList(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}
