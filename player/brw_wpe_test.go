package main

// brw_wpe_test.go — handing pages to WPE Browser: what counts as installed,
// what reaches launch.sh as its address, the environment WPE starts with, and
// what the "needs a full web browser" card offers in each state.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useWpeTestCard points the WPE seams at a temporary SD card on a pretend
// handheld, restoring them when the test ends.
func useWpeTestCard(t *testing.T, handheld, supported bool) string {
	t.Helper()
	root := t.TempDir()
	prevHandheld, prevSupported, prevRoot := brwWpeHandheldFn, brwWpeSupportedFn, brwWpeSDRootFn
	brwWpeHandheldFn = func() bool { return handheld }
	brwWpeSupportedFn = func() bool { return supported }
	brwWpeSDRootFn = func() string { return root }
	t.Cleanup(func() {
		brwWpeHandheldFn, brwWpeSupportedFn, brwWpeSDRootFn = prevHandheld, prevSupported, prevRoot
		brwWpeInvalidate()
	})
	return root
}

func writeWpeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBrwWpeProbeSeesTheInstall(t *testing.T) {
	root := useWpeTestCard(t, true, true)
	s := brwWpeProbe()
	if !s.Handheld || !s.Supported || s.Installed || s.offer() != brwWpeOfferGet {
		t.Fatalf("empty card: %+v offer %v, want installable", s, s.offer())
	}
	dir := filepath.Join(root, "Apps", "WPE")
	if s.Dir != dir {
		t.Fatalf("Dir = %q, want %q", s.Dir, dir)
	}
	writeWpeTestFile(t, filepath.Join(dir, "launch.sh"), "#!/bin/sh\n")
	s = brwWpeProbe()
	if !s.Installed || s.Tag != "" || s.offer() != brwWpeOfferOpen {
		t.Fatalf("manual install: %+v, want installed without a tag", s)
	}
	writeWpeTestFile(t, filepath.Join(dir, brwWpeReleaseFile), "v0.3.1\n")
	if s = brwWpeProbe(); s.Tag != "v0.3.1" {
		t.Fatalf("Tag = %q, want v0.3.1", s.Tag)
	}
	writeWpeTestFile(t, filepath.Join(dir, brwWpeReleaseFile), "../../etc/passwd\n")
	if s = brwWpeProbe(); s.Tag != "" {
		t.Fatalf("a release file that is not a tag gave Tag %q", s.Tag)
	}
	// A folder named launch.sh is not a launcher.
	if err := os.Remove(filepath.Join(dir, "launch.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "launch.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s = brwWpeProbe(); s.Installed {
		t.Fatalf("a launch.sh folder counted as installed")
	}
}

func TestBrwWpeOffers(t *testing.T) {
	cases := []struct {
		name string
		s    brwWpeStatus
		want brwWpeOffer
	}{
		{"pc", brwWpeStatus{}, brwWpeOfferNone},
		{"pc with a stray folder", brwWpeStatus{Installed: true}, brwWpeOfferNone},
		{"installed", brwWpeStatus{Handheld: true, Supported: true, Installed: true}, brwWpeOfferOpen},
		{"installed while an update downloads", brwWpeStatus{Handheld: true, Installed: true, Installing: true}, brwWpeOfferOpen},
		{"downloading", brwWpeStatus{Handheld: true, Supported: true, Installing: true}, brwWpeOfferBusy},
		{"installable", brwWpeStatus{Handheld: true, Supported: true}, brwWpeOfferGet},
		{"unsupported model", brwWpeStatus{Handheld: true}, brwWpeOfferUnsupported},
		{"manual install on an unsupported model", brwWpeStatus{Handheld: true, Installed: true}, brwWpeOfferOpen},
	}
	for _, c := range cases {
		if got := c.s.offer(); got != c.want {
			t.Errorf("%s: offer = %v, want %v", c.name, got, c.want)
		}
	}
	ready := brwWpeStatus{Handheld: true, Installed: true, Tag: "v0.3.0", Latest: "v0.3.1"}
	if !ready.UpdateReady() {
		t.Errorf("v0.3.0 installed with v0.3.1 out should offer an update")
	}
	for _, s := range []brwWpeStatus{
		{Handheld: true, Installed: true, Tag: "v0.3.1", Latest: "v0.3.1"},
		{Handheld: true, Installed: true, Latest: "v0.3.1"},
		{Handheld: true, Installed: true, Tag: "v0.3.1"},
		{Handheld: true, Tag: "v0.3.0", Latest: "v0.3.1"},
	} {
		if s.UpdateReady() {
			t.Errorf("%+v should not offer an update", s)
		}
	}
}

func TestBrwWpeModelSupported(t *testing.T) {
	for d, want := range map[DeviceModel]bool{
		DeviceTrimuiSmartPro:  true,
		DeviceTrimuiBrick:     true,
		DeviceTrimuiBrickPro:  true,
		DeviceTrimuiHammer:    true,
		DeviceTrimuiSmartProS: false,
		DeviceDevBuild:        false,
		DeviceUnknown:         false,
	} {
		if got := brwWpeModelSupported(d); got != want {
			t.Errorf("brwWpeModelSupported(%s) = %v, want %v", d, got, want)
		}
	}
}

func TestBrwWpeTarget(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://www.google.com/search?q=trimui+brick", "https://www.google.com/search?q=trimui+brick"},
		{"  HTTPS://Example.com/a b  ", "https://Example.com/a%20b"},
		{"http://example.com", "http://example.com"},
		{"", ""},
		{"about:blank", ""},
		{"file:///etc/passwd", ""},
		{"javascript:alert(1)", ""},
		{"https://user:pass@example.com/", ""},
		{"https:///no-host", ""},
		{"https://example.com/\nrm -rf /", ""},
		{"https://example.com/" + strings.Repeat("a", brwWpeMaxURL), ""},
		{"-rf", ""},
	}
	for _, c := range cases {
		if got := brwWpeTarget(c.in); got != c.want {
			t.Errorf("brwWpeTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func handheldWallButtons() []brwCardButton {
	return []brwCardButton{
		{action: brwActWallSearch, label: "Search on Startpage", icon: "search"},
		{action: brwActWallAlways, label: "Always use Startpage", icon: "check"},
		{action: brwActWallEngines, label: "Change engine", icon: "settings"},
	}
}

func cardActions(buttons []brwCardButton) []brwCardAction {
	out := make([]brwCardAction, 0, len(buttons))
	for _, b := range buttons {
		out = append(out, b.action)
	}
	return out
}

func TestBrwWpeWallButtonsFor(t *testing.T) {
	base := handheldWallButtons()
	same := func(got, want []brwCardAction) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	open := brwWpeWallButtonsFor(base, brwWpeStatus{Handheld: true, Supported: true, Installed: true})
	if got, want := cardActions(open), []brwCardAction{brwActWpeOpen, brwActWallSearch, brwActWallEngines}; !same(got, want) {
		t.Errorf("installed: %v, want %v", got, want)
	}
	if open[0].label != "Open in WPE Browser" {
		t.Errorf("the first button reads %q", open[0].label)
	}
	get := brwWpeWallButtonsFor(base, brwWpeStatus{Handheld: true, Supported: true})
	if got, want := cardActions(get), []brwCardAction{brwActWallSearch, brwActWallAlways, brwActWpeGet}; !same(got, want) {
		t.Errorf("installable: %v, want %v", got, want)
	}
	for _, s := range []brwWpeStatus{{}, {Handheld: true}, {Handheld: true, Supported: true, Installing: true}} {
		if got := brwWpeWallButtonsFor(base, s); !same(cardActions(got), cardActions(base)) {
			t.Errorf("%+v changed the card: %v", s, cardActions(got))
		}
	}
	for _, buttons := range [][]brwCardButton{open, get} {
		if len(buttons) > len(brwCard.rects) {
			t.Errorf("%d buttons do not fit the card's %d places", len(buttons), len(brwCard.rects))
		}
	}
	if base[0].action != brwActWallSearch || len(base) != 3 {
		t.Errorf("the card's own list was modified")
	}
}

func TestBrwWpeWallDetailFor(t *testing.T) {
	wayOut := "Startpage shows Google's results without the check."
	detail := "Its robot check turns away small script engines like this one. " + wayOut
	got := brwWpeWallDetailFor(detail, wayOut, "Startpage", true)
	want := "Its robot check turns away small script engines like this one. Open it in WPE Browser, or try Startpage: Google's results without the check."
	if got != want {
		t.Errorf("open: %q\nwant %q", got, want)
	}
	if got := brwWpeWallDetailFor(detail, wayOut, "Startpage", false); got != detail {
		t.Errorf("not installed changed the detail: %q", got)
	}
	if got := brwWpeWallDetailFor("Something else entirely.", wayOut, "Startpage", true); got != "Something else entirely." {
		t.Errorf("a detail without the way-out sentence changed: %q", got)
	}
}

func TestBrwWpeSettingsWords(t *testing.T) {
	cases := []struct {
		s     brwWpeStatus
		value string
	}{
		{brwWpeStatus{Handheld: true, Supported: true}, "Not installed"},
		{brwWpeStatus{Handheld: true, Supported: true, Installing: true}, "Downloading…"},
		{brwWpeStatus{Handheld: true, Installed: true}, "Installed"},
		{brwWpeStatus{Handheld: true, Installed: true, Tag: "v0.3.1"}, "v0.3.1"},
		{brwWpeStatus{Handheld: true, Installed: true, Tag: "v0.3.0", Latest: "v0.3.1"}, "Update: v0.3.1"},
		{brwWpeStatus{Handheld: true}, "Not available"},
	}
	for _, c := range cases {
		if got := brwWpeSettingsValue(c.s); got != c.value {
			t.Errorf("brwWpeSettingsValue(%+v) = %q, want %q", c.s, got, c.value)
		}
		if brwWpeSettingsDetail(c.s) == "" {
			t.Errorf("brwWpeSettingsDetail(%+v) is empty", c.s)
		}
	}
}
