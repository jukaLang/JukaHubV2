package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTVSysfsParsers covers the readers of the kernel's video-out status
// files (tvmode_sysfs.go): outside data whose format a wrong guess turns into
// a TV that is never seen, or one seen when there is none.
func TestTVSysfsParsers(t *testing.T) {
	for _, c := range []struct {
		mode, virtual string
		w, h          int
		ok            bool
	}{
		{"U:1920x1080p-60\n", "", 1920, 1080, true},
		{"", "1024,1536\n", 1024, 768, true}, // two buffers
		{"", "1280,2160", 1280, 720, true},   // three buffers
		{"", "1024,768", 1024, 768, true},
		{"", "720,1281", 0, 0, false},
		{"", "", 0, 0, false},
	} {
		w, h, ok := tvParseFBMode(c.mode, c.virtual)
		if w != c.w || h != c.h || ok != c.ok {
			t.Errorf("tvParseFBMode(%q, %q) = %d, %d, %t; want %d, %d, %t", c.mode, c.virtual, w, h, ok, c.w, c.h, c.ok)
		}
	}

	names, on := tvParseExtconState("USB=1\nUSB-HOST=0\nDP=1\nHDMI=0\n", "")
	if strings.Join(names, ",") != "DP,HDMI" || len(on) != 2 || !on[0] || on[1] {
		t.Errorf("extcon state: %v %v", names, on)
	}
	if names, on := tvParseExtconState("1\n", "hdmi"); len(names) != 1 || !on[0] {
		t.Errorf("single-cable extcon: %v %v", names, on)
	}
	if names, _ := tvParseExtconState("USB=1\n", "usb-otg"); len(names) != 0 {
		t.Errorf("a USB cable is not a display: %v", names)
	}

	dump := "screen 0:\nde_rate 696000000 hz, ref_fps:60\n\tlcd output\tbacklight( 50)\tfps:60.5\t 1024x 768\n" +
		"screen 1:\n\thdmi output mode(10)\tfps:60.0\t1920x1080\n"
	outs := tvParseDispSys(dump)
	if len(outs) != 2 || outs[0].Kind != "lcd" || outs[0].W != 1024 || outs[0].H != 768 ||
		outs[1].Screen != 1 || outs[1].Kind != "hdmi" || outs[1].W != 1920 || outs[1].H != 1080 {
		t.Errorf("disp dump: %+v", outs)
	}

	if !tvDRMExternal("card0-HDMI-A-1") || !tvDRMExternal("card1-DP-2") || tvDRMExternal("card0-eDP-1") ||
		tvDRMExternal("card0-DSI-1") || tvDRMExternal("card0-DPI-1") {
		t.Error("DRM connector kinds")
	}

	// A whole tree: a DisplayPort partner with hot-plug, a dead extcon cable,
	// and the panel-size framebuffer.
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("class/typec/port0/data_role", "host\n")
	write("class/typec/port0-partner/port0-partner.0/svid", "ff01\n")
	write("class/typec/port0-partner/port0-partner.0/displayport/hpd", "yes\n")
	write("class/extcon/extcon0/state", "USB=0\nDP=0\n")
	write("class/graphics/fb0/virtual_size", "1024,1536\n")
	ev := tvProbeRoot(root, 1024, 768)
	if !ev.connected() || len(ev.Sources) != 1 || !strings.Contains(ev.Sources[0], "DisplayPort") {
		t.Errorf("tree: sources %v", ev.Sources)
	}
	if strings.Join(ev.Paths, ",") != "typec port0,extcon0 DP" || ev.FBW != 1024 || ev.FBH != 768 {
		t.Errorf("tree: paths %v, fb %dx%d", ev.Paths, ev.FBW, ev.FBH)
	}

	// Nothing but a framebuffer that left the panel's size.
	root = t.TempDir()
	write("class/graphics/fb0/mode", "U:1920x1080p-60\n")
	if ev := tvProbeRoot(root, 1024, 768); !ev.connected() || len(ev.Paths) != 0 {
		t.Errorf("framebuffer only: %+v", ev)
	}
}
