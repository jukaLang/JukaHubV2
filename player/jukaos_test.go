package main

// jukaos_test.go — JukaHub OS's parsers of outside data and its persistence:
// layer.json checks (path escapes, refused targets, the C-runtime gate), a
// module's .modinfo/vermagic, the journal and os.json. GOOS-neutral, like
// jukaos.go.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jukaosTestELF builds a minimal little-endian ELF64 relocatable object for
// machine with one .modinfo section holding modinfo.
func jukaosTestELF(machine uint16, modinfo string) []byte {
	shstr := "\x00.modinfo\x00.shstrtab\x00"
	var b bytes.Buffer
	modOff := uint64(64)
	strOff := modOff + uint64(len(modinfo))
	shOff := (strOff + uint64(len(shstr)) + 7) &^ 7
	le := binary.LittleEndian
	ident := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	b.Write(ident)
	w := func(v any) { _ = binary.Write(&b, le, v) }
	w(uint16(1))  // e_type: ET_REL
	w(machine)    // e_machine
	w(uint32(1))  // e_version
	w(uint64(0))  // e_entry
	w(uint64(0))  // e_phoff
	w(shOff)      // e_shoff
	w(uint32(0))  // e_flags
	w(uint16(64)) // e_ehsize
	w(uint16(0))  // e_phentsize
	w(uint16(0))  // e_phnum
	w(uint16(64)) // e_shentsize
	w(uint16(3))  // e_shnum
	w(uint16(2))  // e_shstrndx
	b.WriteString(modinfo)
	b.WriteString(shstr)
	for uint64(b.Len()) < shOff {
		b.WriteByte(0)
	}
	section := func(name, typ uint32, off, size uint64) {
		w(name)
		w(typ)
		w(uint64(0)) // flags
		w(uint64(0)) // addr
		w(off)
		w(size)
		w(uint32(0)) // link
		w(uint32(0)) // info
		w(uint64(1)) // addralign
		w(uint64(0)) // entsize
	}
	section(0, 0, 0, 0)
	section(1, 1, modOff, uint64(len(modinfo)))
	section(10, 3, strOff, uint64(len(shstr)))
	return b.Bytes()
}

const jukaosTestModinfo = "license=GPL\x00vermagic=4.9.191 SMP preempt mod_unload aarch64\x00name=rtl8xxx-wifi\x00"

func TestJukaosModinfoVermagic(t *testing.T) {
	info, machine, err := jukaosModinfo(bytes.NewReader(jukaosTestELF(183, jukaosTestModinfo)))
	if err != nil {
		t.Fatalf("modinfo: %v", err)
	}
	if machine != 183 || info["vermagic"] != "4.9.191 SMP preempt mod_unload aarch64" || info["license"] != "GPL" {
		t.Fatalf("modinfo = %v machine %v", info, machine)
	}
	if got := jukaosModuleName(info, "drivers/x.ko"); got != "rtl8xxx_wifi" {
		t.Fatalf("module name = %q", got)
	}
	if got := jukaosModuleName(map[string]string{}, "drivers/my-drv.ko"); got != "my_drv" {
		t.Fatalf("module name from file = %q", got)
	}
	for _, c := range []struct {
		release, machine string
		ok               bool
	}{
		{"4.9.191", "aarch64", true},
		{"4.9.170", "aarch64", false},
		{"4.9.191-rc1", "aarch64", false},
		{"4.9.191", "x86_64", false},
		{"", "aarch64", false},
	} {
		if ok, why := jukaosVermagicFits(info["vermagic"], c.release, c.machine); ok != c.ok {
			t.Errorf("vermagic on %s/%s = %v (%s), want %v", c.release, c.machine, ok, why, c.ok)
		}
	}
	if _, _, err := jukaosModinfo(bytes.NewReader([]byte("not an elf file at all, just text"))); err == nil {
		t.Fatal("a text file passed as a module")
	}

	// Through the layer check: inside the layer, right kernel, right machine.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.ko"), jukaosTestELF(183, jukaosTestModinfo), 0o644); err != nil {
		t.Fatal(err)
	}
	base, _ := filepath.EvalSymlinks(dir)
	h := jukaosHost{Machine: "aarch64", Release: "4.9.191"}
	if m, err := jukaosCheckModule(h, base, "x.ko"); err != nil || m.Name != "rtl8xxx_wifi" {
		t.Fatalf("module check = %+v, %v", m, err)
	}
	h.Release = "5.10.0"
	if _, err := jukaosCheckModule(h, base, "x.ko"); err == nil {
		t.Fatal("a module for another kernel passed")
	}
	h = jukaosHost{Machine: "x86_64", Release: "4.9.191"}
	if _, err := jukaosCheckModule(h, base, "x.ko"); err == nil {
		t.Fatal("an aarch64 module passed on x86_64")
	}
	if _, err := jukaosCheckModule(h, base, "../x.ko"); err == nil {
		t.Fatal("a module outside the layer passed")
	}
}

// jukaosTestWrite writes a file, making its folder.
func jukaosTestWrite(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJukaosLayerChecks(t *testing.T) {
	root := t.TempDir()
	sys := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	jukaosTestWrite(t, sys("/usr/lib/libfoo.so.1"), []byte("old"))
	jukaosTestWrite(t, sys("/usr/lib/libc.so.6"), []byte("libc"))
	jukaosTestWrite(t, sys("/usr/share/emu/emu.cfg"), []byte("cfg"))
	jukaosTestWrite(t, sys("/proc/cpuinfo"), []byte("cpu"))
	jukaosTestWrite(t, sys("/mnt/SDCARD/Apps/JukaHub/JukaHub"), []byte("bin"))
	jukaosTestWrite(t, sys("/mnt/SDCARD/Roms/a.gba"), []byte("rom"))
	layer := filepath.Join(t.TempDir(), "newlibs")
	jukaosTestWrite(t, filepath.Join(layer, "files", "libfoo.so.1"), []byte("new"))
	jukaosTestWrite(t, filepath.Join(layer, "files", "libc.so.6"), []byte("newlibc"))
	jukaosTestWrite(t, filepath.Join(layer, "files", "emu", "emu.cfg"), []byte("cfg2"))
	jukaosTestWrite(t, filepath.Join(layer, "files", "arm64.so"), jukaosTestELF(183, "x=y\x00"))
	jukaosTestWrite(t, filepath.Join(layer, "lib", "libSDL2-2.0.so.0"), []byte("sdl"))
	jukaosTestWrite(t, filepath.Join(filepath.Dir(layer), "outside.so"), []byte("x"))
	base, err := filepath.EvalSymlinks(layer)
	if err != nil {
		t.Fatal(err)
	}
	h := jukaosHost{Root: root, Machine: "aarch64", Release: "4.9.191", AppDir: "/mnt/SDCARD/Apps/JukaHub", SDRoot: "/mnt/SDCARD"}

	for _, c := range []struct {
		src, dst string
		core     bool
		ok       bool
	}{
		{"files/libfoo.so.1", "/usr/lib/libfoo.so.1", false, true},
		{"files/emu", "/usr/share/emu", false, true},
		{"files/emu/emu.cfg", "/mnt/SDCARD/Roms/a.gba", false, true}, // inside the card is fine
		{"../outside.so", "/usr/lib/libfoo.so.1", false, false},
		{"files/../../outside.so", "/usr/lib/libfoo.so.1", false, false},
		{"/etc/passwd", "/usr/lib/libfoo.so.1", false, false},
		{"files/missing.so", "/usr/lib/libfoo.so.1", false, false},
		{"files/libfoo.so.1", "usr/lib/libfoo.so.1", false, false},
		{"files/libfoo.so.1", "/usr/lib/../../proc/cpuinfo", false, false},
		{"files/libfoo.so.1", "/proc/cpuinfo", false, false},
		{"files/emu", "/", false, false},
		{"files/emu", "/mnt/SDCARD", false, false},
		{"files/emu", "/mnt", false, false},
		{"files/libfoo.so.1", "/mnt/SDCARD/Apps/JukaHub/JukaHub", false, false},
		{"files/libfoo.so.1", "/usr/lib/missing.so", false, false},
		{"files/libfoo.so.1", "/usr/share/emu", false, false}, // file over a folder
		{"files/libc.so.6", "/usr/lib/libc.so.6", false, false},
		{"files/libc.so.6", "/usr/lib/libc.so.6", true, true},
		{"files/emu", "/usr/lib", false, false}, // holds libc.so.6
		{"files/emu", "/usr/lib", true, true},
		{"files/emu", "/usr", false, false}, // libc.so.6 one level down
	} {
		_, err := jukaosCheckBind(h, base, jukaosBindSpec{Src: c.src, Dst: c.dst}, c.core)
		if (err == nil) != c.ok {
			t.Errorf("bind %s -> %s (core %v): err %v, want ok=%v", c.src, c.dst, c.core, err, c.ok)
		}
	}

	// A symlink out of the layer is refused where the host can make one.
	if err := os.Symlink(filepath.Join(filepath.Dir(layer), "outside.so"), filepath.Join(layer, "files", "link.so")); err == nil {
		if _, err := jukaosCheckBind(h, base, jukaosBindSpec{Src: "files/link.so", Dst: "/usr/lib/libfoo.so.1"}, false); err == nil {
			t.Error("a symlink out of the layer passed")
		}
	}

	// An aarch64 library over a file of a machine without ELF on its target
	// is compared with the host.
	hx := h
	hx.Machine = "x86_64"
	if _, err := jukaosCheckBind(hx, base, jukaosBindSpec{Src: "files/arm64.so", Dst: "/usr/lib/libfoo.so.1"}, false); err == nil {
		t.Error("an aarch64 library passed on x86_64")
	}
	if _, err := jukaosCheckBind(h, base, jukaosBindSpec{Src: "files/arm64.so", Dst: "/usr/lib/libfoo.so.1"}, false); err != nil {
		t.Errorf("an aarch64 library on aarch64: %v", err)
	}

	// The whole layer.json: a bad part is a note, a wrong machine a problem.
	jukaosTestWrite(t, filepath.Join(layer, "layer.json"), []byte(`{
  "name": "New libraries", "version": "1.2", "arch": "aarch64",
  "binds": [{"src": "files/libfoo.so.1", "dst": "/usr/lib/libfoo.so.1"},
            {"src": "files/libc.so.6", "dst": "/usr/lib/libc.so.6"}],
  "libs": ["lib", "../lib"], "children": true,
  "sysfs": {"/proc/sys/vm/swappiness": "10", "/proc/sys/vm/drop_caches": "3",
            "/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor": "performance"},
  "future_field": 1
}`))
	l := jukaosLoadLayer(h, "newlibs", layer)
	if l.Problem != "" || len(l.Binds) != 1 || len(l.LibDirs) != 1 || len(l.Sysfs) != 1 || len(l.Notes) != 4 {
		t.Fatalf("layer = problem %q, %d binds, %d libs, %d values, notes %q", l.Problem, len(l.Binds), len(l.LibDirs), len(l.Sysfs), l.Notes)
	}
	if l.Binds[0].Dst != "/usr/lib/libfoo.so.1" || l.Sysfs[0] != (jukaosSysfsWrite{Path: "/proc/sys/vm/swappiness", Value: "10"}) {
		t.Fatalf("layer parts = %+v %+v", l.Binds[0], l.Sysfs[0])
	}
	jukaosTestWrite(t, filepath.Join(layer, "layer.json"), []byte(`{"name": "PC libs", "arch": "x86_64", "libs": ["lib"]}`))
	if l := jukaosLoadLayer(h, "newlibs", layer); l.Problem == "" {
		t.Fatal("an x86_64 layer was usable on aarch64")
	}
	jukaosTestWrite(t, filepath.Join(layer, "layer.json"), []byte(`{"name": "armhf libs", "arch": "armhf", "libs": ["lib"]}`))
	if l := jukaosLoadLayer(h, "newlibs", layer); l.Problem != "" {
		t.Fatalf("a 32-bit ARM layer on aarch64: %s", l.Problem)
	}
	jukaosTestWrite(t, filepath.Join(layer, "layer.json"), []byte(`{"name": `))
	if l := jukaosLoadLayer(h, "newlibs", layer); l.Problem == "" {
		t.Fatal("a broken layer.json was usable")
	}

	// A driver layer for one model and kernel: anywhere else unusable with the
	// reason; its values only where the file exists, a "*" expanded and
	// checked again.
	jukaosTestWrite(t, sys("/sys/module/usbcore/parameters/autosuspend"), []byte("2"))
	jukaosTestWrite(t, sys("/sys/class/devfreq/1800000.gpu/max_freq"), []byte("696000000"))
	jukaosTestWrite(t, filepath.Join(layer, "layer.json"), []byte(`{"name": "A523 driver layer", "kind": "drivers",
  "devices": ["trimui-smart-pro-s"], "kernel": "5.15",
  "sysfsIfPresent": {"/sys/module/usbcore/parameters/autosuspend": "-1",
                     "/sys/module/bluetooth/parameters/disable_ertm": "Y",
                     "/sys/class/devfreq/*gpu*/max_freq": "600000000",
                     "/sys/class/devfreq/*/max_freq": "1"}}`))
	hs := h
	hs.Model, hs.ModelCode, hs.Release = "trimui-smart-pro-s", "TG5050", "5.15.147"
	l = jukaosLoadLayer(hs, "drivers-a523", layer)
	if l.Problem != "" || len(l.Sysfs) != 2 || l.Absent != 1 || len(l.Notes) != 1 ||
		l.Sysfs[0].Path != "/sys/class/devfreq/1800000.gpu/max_freq" || l.Spec.Kind != jukaosKindDrivers {
		t.Fatalf("driver layer = problem %q, values %+v, absent %d, notes %q", l.Problem, l.Sysfs, l.Absent, l.Notes)
	}
	for _, c := range []struct {
		model, code, release string
		want                 string
	}{
		{"trimui-brick", "TG3040", "5.15.147", "built for Smart Pro S, this is Brick"},
		{"unknown", "", "5.15.147", "could not be told"},
		{"trimui-smart-pro-s", "TG5050", "5.150.1", "built for kernel 5.15"},
		{"trimui-smart-pro-s", "TG5050", "4.9.191", "built for kernel 5.15"},
	} {
		hc := hs
		hc.Model, hc.ModelCode, hc.Release = c.model, c.code, c.release
		if l := jukaosLoadLayer(hc, "drivers-a523", layer); !strings.Contains(l.Problem, c.want) {
			t.Errorf("driver layer on %s %s = problem %q, want %q", c.model, c.release, l.Problem, c.want)
		}
	}
	if why := jukaosDeviceFits([]string{"tg3040"}, jukaosHost{Model: "trimui-hammer", ModelCode: "TG3040"}); why != "" {
		t.Errorf("a TG3040 layer on the Hammer: %s", why)
	}
	if spec, err := jukaosDecodeLayerSpec([]byte(`{"devices": [" "]}`)); err != nil || jukaosDeviceFits(spec.Devices, hs) == "" {
		t.Errorf("a blank devices list widened the layer: %+v %v", spec, err)
	}

	for p, ok := range map[string]bool{
		"/proc/sys/vm/swappiness":                               true,
		"/proc/sys/vm/drop_caches":                              false,
		"/proc/sys/kernel/sched_latency_ns":                     true,
		"/proc/sys/kernel/panic":                                false,
		"/sys/block/mmcblk0/queue/scheduler":                    true,
		"/sys/block/mmcblk0/queue/../../../power/state":         false,
		"/sys/kernel/mm/transparent_hugepage/enabled":           true,
		"/sys/module/snd/parameters/slots":                      true,
		"/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor": false,
		"/sys/power/state":                                      false,
		"proc/sys/vm/swappiness":                                false,
	} {
		if got := jukaosSysfsAllowed(p); got != ok {
			t.Errorf("sysfs %s allowed = %v, want %v", p, got, ok)
		}
	}
}

func TestJukaosJournalAndState(t *testing.T) {
	j := jukaosJournal{PID: 42, BootID: "b", Entries: []jukaosJournalEntry{
		{Kind: jukaosKindModule, Name: "drv_a", Path: "/x/a.ko"},
		{Kind: jukaosKindSysfs, Path: "/proc/sys/vm/swappiness", Old: "60", New: "10"},
		{Kind: jukaosKindSysfs, Path: "/sys/block/mmcblk0/queue/scheduler", Old: "cfq", New: "deadline"},
		{Kind: "shell", Path: "rm -rf /"},
		{Kind: jukaosKindModule, Name: "../ext4", Path: "/x/b.ko"},
		{Kind: jukaosKindSysfs, Path: "/sys/power/state", Old: "on", New: "mem"},
	}}
	data, err := jukaosEncodeJournal(j)
	if err != nil {
		t.Fatal(err)
	}
	back, err := jukaosDecodeJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.PID != 42 || back.BootID != "b" || len(back.Entries) != 3 || back.Entries[2].New != "deadline" {
		t.Fatalf("journal round trip = %+v", back)
	}
	if _, err := jukaosDecodeJournal([]byte("{")); err == nil {
		t.Fatal("a broken journal decoded")
	}

	values := map[string]string{
		"/proc/sys/vm/swappiness":            "30", // changed by someone else since
		"/sys/block/mmcblk0/queue/scheduler": "noop [deadline] cfq",
	}
	var order []string
	ops := jukaosRevertOps{
		ReadValue: func(p string) (string, error) { return values[p], nil },
		WriteValue: func(p, v string) error {
			order = append(order, p)
			values[p] = v
			return nil
		},
		UnloadModule: func(name string) error {
			order = append(order, "rmmod "+name)
			return nil
		},
	}
	report := jukaosRevertEntries(back.Entries, ops)
	if len(report) != 3 || values["/proc/sys/vm/swappiness"] != "30" || values["/sys/block/mmcblk0/queue/scheduler"] != "cfq" {
		t.Fatalf("revert = %q, values %v", report, values)
	}
	if strings.Join(order, ",") != "/sys/block/mmcblk0/queue/scheduler,rmmod drv_a" {
		t.Fatalf("revert order = %v", order)
	}

	// os.json: the form launch.sh greps for, and ids it must not misread.
	on := true
	out, err := jukaosEncodeState(jukaosState{Enabled: true, Layers: map[string]bool{"jukahub-libs": true}, DropCaches: &on})
	if err != nil || !bytes.Contains(out, []byte(`"enabled": true`)) {
		t.Fatalf("os.json = %s, %v", out, err)
	}
	s, err := jukaosDecodeState([]byte(`{"enabled": true, "layers": {"enabled": true, "../x": true, "ok-1": true}}`))
	if err != nil || !s.Enabled || len(s.Layers) != 1 || !s.Layers["ok-1"] || !s.jukaosDropCachesOn() {
		t.Fatalf("os.json decode = %+v, %v", s, err)
	}
	off, err := jukaosEncodeState(jukaosState{Layers: map[string]bool{"a": true}})
	if err != nil || bytes.Contains(off, []byte(`"enabled": true`)) {
		t.Fatalf("os.json off = %s", off)
	}
	// The OS package's os.json (trimui/make-os-package.sh writes it by hand):
	// read as OS mode on, and the edition survives a save from Settings.
	pkg, err := jukaosDecodeState([]byte("{\n  \"enabled\": true,\n  \"edition\": \"os\"\n}\n"))
	if err != nil || !pkg.Enabled || pkg.Edition != jukaosEditionOS {
		t.Fatalf("package os.json = %+v, %v", pkg, err)
	}
	pkg.Enabled = false
	again, err := jukaosEncodeState(pkg)
	if err != nil || !bytes.Contains(again, []byte(`"edition": "os"`)) {
		t.Fatalf("package os.json re-saved = %s, %v", again, err)
	}
	// Skip the stock menu: on unless os.json says `"fastBoot": false`, the
	// form the boot hook greps for (trimuiAutostartTemplate).
	if !pkg.jukaosFastBootOn() {
		t.Fatal("fastBoot is off without being set")
	}
	no := false
	pkg.FastBoot = &no
	slow, err := jukaosEncodeState(pkg)
	if err != nil || !bytes.Contains(slow, []byte(`"fastBoot": false`)) {
		t.Fatalf("os.json with fastBoot off = %s, %v", slow, err)
	}
	if back, err := jukaosDecodeState(slow); err != nil || back.jukaosFastBootOn() {
		t.Fatalf("fastBoot off did not survive a save: %+v, %v", back, err)
	}
}
