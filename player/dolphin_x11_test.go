package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDolphinNeedsSetupWithMissingCoreAndExistingSys(t *testing.T) {
	tests := []struct {
		name      string
		need      dolphinNeeds
		setup     bool
		staleOnly bool
	}{
		{"ready", dolphinNeeds{}, false, false},
		{"core removed but Sys remains", dolphinNeeds{CoreMissing: true}, true, false},
		{"old core only", dolphinNeeds{CoreStale: true}, true, true},
		{"old core and missing Sys", dolphinNeeds{CoreStale: true, SysMissing: true}, true, false},
		{"desktop libraries missing", dolphinNeeds{X11Missing: true}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dolphinNeedsSetup(tt.need); got != tt.setup {
				t.Errorf("setup = %t, want %t", got, tt.setup)
			}
			if got := dolphinStaleOnly(tt.need); got != tt.staleOnly {
				t.Errorf("stale only = %t, want %t", got, tt.staleOnly)
			}
		})
	}
}

func TestDolphinX11CoreNeeds(t *testing.T) {
	core := filepath.Join(t.TempDir(), "dolphin_libretro.so")
	writeTestFile(t, core, buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, needed: []string{"libEGL.so.1", "libX11.so.6"},
	}))
	if !dolphinX11CoreNeeds(core) {
		t.Fatal("Dolphin's direct libX11 dependency was missed")
	}
	writeTestFile(t, core, buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, needed: []string{"libEGL.so.1"},
	}))
	if dolphinX11CoreNeeds(core) {
		t.Fatal("a core without libX11 should not need the bundle")
	}
}

func TestDolphinX11ExtractChecksELFBeforeReplacing(t *testing.T) {
	dir := t.TempDir()
	p := dolphinX11Package{name: "libX11.so.6", member: "libX11.so.6.3.0"}
	data := buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, needed: []string{p.name, p.name},
	})
	// The minimal shared-object builder gives us two dynamic entries. Change
	// the second one from DT_NEEDED to DT_SONAME while retaining its string.
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	section := f.Section(".dynamic")
	if section == nil {
		t.Fatal("test ELF has no dynamic section")
	}
	binary.LittleEndian.PutUint64(data[int(section.Offset)+16:], uint64(elf.DT_SONAME))
	p.expanded = int64(len(data))
	deb := dolphinX11TestDeb(t, dir, p.member, data)
	if err := dolphinX11Extract(context.Background(), deb, dir, p); err != nil {
		t.Fatalf("extract valid ARM64 library: %v", err)
	}
	out := filepath.Join(dir, p.name)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("installed bytes differ: %v", err)
	}

	bad := bytes.Clone(data)
	binary.LittleEndian.PutUint16(bad[18:], uint16(elf.EM_X86_64))
	deb = dolphinX11TestDeb(t, dir, p.member, bad)
	if err := dolphinX11Extract(context.Background(), deb, dir, p); err == nil {
		t.Fatal("wrong-architecture ELF replaced the installed library")
	}
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("existing library changed after rejection: %v", err)
	}
}

func dolphinX11TestDeb(t *testing.T, dir, member string, data []byte) string {
	t.Helper()
	var packed bytes.Buffer
	tw := tar.NewWriter(&packed)
	name := "./usr/lib/aarch64-linux-gnu/" + member
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(packed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	archive.WriteString("!<arch>\n")
	fmt.Fprintf(&archive, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", "data.tar.gz/", 0, 0, 0, "100644", compressed.Len())
	archive.Write(compressed.Bytes())
	if compressed.Len()%2 != 0 {
		archive.WriteByte('\n')
	}
	path := filepath.Join(dir, "x11-test.deb")
	if err := os.WriteFile(path, archive.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
