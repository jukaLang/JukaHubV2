package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestWineFreeTypeFromRequiresArm64Soname(t *testing.T) {
	data := buildTestELF(t, testELF{
		machine: elf.EM_AARCH64, needed: []string{"libfreetype.so.6", "libfreetype.so.6"},
	})
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	section := f.Section(".dynamic")
	if section == nil {
		t.Fatal("test ELF has no dynamic section")
	}
	// The second dynamic entry names the SONAME instead of a dependency.
	binary.LittleEndian.PutUint64(data[int(section.Offset)+16:], uint64(elf.DT_SONAME))
	root := t.TempDir()
	good := filepath.Join(root, "good.so")
	if err := os.WriteFile(good, data, 0o600); err != nil {
		t.Fatal(err)
	}
	wrongArch := bytes.Clone(data)
	binary.LittleEndian.PutUint16(wrongArch[18:], uint16(elf.EM_X86_64))
	bad := filepath.Join(root, "bad.so")
	if err := os.WriteFile(bad, wrongArch, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := wineFreeTypeFrom([]string{bad, good}); got != good {
		t.Fatalf("selected %q, want the ARM64 FreeType at %q", got, good)
	}
	if got := wineFreeTypeFrom([]string{bad}); got != "" {
		t.Fatalf("selected wrong-architecture FreeType %q", got)
	}
}
