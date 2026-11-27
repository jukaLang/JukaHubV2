package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three libraries PortMaster's LÖVE 11.5 runtime loads from the firmware
// (liblove-11.5.so's DT_NEEDED), and the ones JukaHub supplies on a firmware
// that has none of them.
func TestPMCodecCoversLibs(t *testing.T) {
	cases := []struct {
		names []string
		want  bool
	}{
		{nil, false},
		{[]string{"libmpg123.so.0"}, true},
		{[]string{"libmpg123.so.0", "libvorbis.so.0", "libvorbisfile.so.3"}, true},
		{[]string{"libmpg123.so.0", "libtheoradec.so.1"}, false},
		{[]string{"libSDL2-2.0.so.0"}, false},
	}
	for _, c := range cases {
		if got := pmCodecCoversLibs(c.names); got != c.want {
			t.Errorf("pmCodecCoversLibs(%v) = %v, want %v", c.names, got, c.want)
		}
	}
}

func TestPMCodecPackagesPinned(t *testing.T) {
	names := map[string]bool{}
	for _, p := range pmCodecPackages {
		names[p.name] = true
		if len(p.sha) != 64 || strings.Trim(p.sha, "0123456789abcdef") != "" {
			t.Errorf("%s: sha %q is not 64 lower-case hex digits", p.name, p.sha)
		}
		if !strings.HasSuffix(p.archive, "_arm64.deb") || !strings.HasPrefix(p.archive, "pool/") {
			t.Errorf("%s: archive %q is not an Ubuntu arm64 .deb", p.name, p.archive)
		}
		if p.size <= 0 || p.expanded <= p.size {
			t.Errorf("%s: sizes %d/%d do not describe a compressed package", p.name, p.size, p.expanded)
		}
		if stem := strings.SplitN(p.name, ".so", 2)[0]; !strings.HasPrefix(p.member, stem+".so") {
			t.Errorf("%s: member %q is not that library", p.name, p.member)
		}
	}
	for _, want := range []string{"libmpg123.so.0", "libvorbis.so.0", "libvorbisfile.so.3"} {
		if !names[want] {
			t.Errorf("the bundle does not install %s", want)
		}
	}
}

func TestPMCodecLibPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "libmpg123.so.0"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !pmCodecLibPresent([]string{dir}, "libmpg123.so.0") {
		t.Error("a library in the folder was not found")
	}
	if pmCodecLibPresent([]string{dir}, "libvorbis.so.0") {
		t.Error("a library that is not there was reported present")
	}
	if pmCodecLibPresent([]string{"", filepath.Join(dir, "missing")}, "libmpg123.so.0") {
		t.Error("an empty or missing folder reported the library present")
	}
}

// The bundle goes after whatever the port already had: PortMaster's own place
// for libraries a CFW may lack, so the firmware's copies win.
func TestPMCodecPathEnvLast(t *testing.T) {
	dir := "/data/private-glibc/portmaster-codecs"
	got := pmCodecPathEnv([]string{"HOME=/", "LD_LIBRARY_PATH=/usr/trimui/lib"}, dir)
	libs, _ := childEnvLookup(got, "LD_LIBRARY_PATH")
	if libs != "/usr/trimui/lib:"+dir {
		t.Errorf("LD_LIBRARY_PATH = %q, want the bundle last", libs)
	}
	if again := pmCodecPathEnv(got, dir); len(again) != len(got) {
		t.Errorf("a second call added the folder again: %v", again)
	}
	none := pmCodecPathEnv([]string{"HOME=/"}, dir)
	if libs, _ := childEnvLookup(none, "LD_LIBRARY_PATH"); libs != dir {
		t.Errorf("LD_LIBRARY_PATH = %q, want only the bundle", libs)
	}
	if same := pmCodecPathEnv(got, ""); len(same) != len(got) {
		t.Errorf("an empty folder changed the environment")
	}
}
