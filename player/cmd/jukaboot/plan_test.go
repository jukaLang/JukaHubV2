package main

import (
	"reflect"
	"sync"
	"testing"
)

// board.env, daemons.list and gpio.list are written by build_image.py from a
// board dump; boot.env by JukaHub or by hand on a PC.
func TestParseBoardFiles(t *testing.T) {
	env := parseBoardEnv("# Written by os/smartpro/build_image.py\n" +
		"DATA_NAME=JUKAHUB\r\n" +
		"DATA_FSTYPE=exfat\n" +
		"MODULES='/lib/modules/4.9.191/pvrsrvkm.ko /lib/modules/4.9.191/dc-sunxi.ko'\n" +
		"RADIO_MODULES=''\n" +
		"MODULE_DEPS='dc-sunxi:pvrsrvkm,dc_sunxi bar:'\n" +
		"WIFI=1\n" +
		"BROKEN='unterminated\n" +
		"lower=1\n")
	want := boardEnv{
		DataName:   "JUKAHUB",
		DataFSType: "exfat",
		Modules:    []string{"/lib/modules/4.9.191/pvrsrvkm.ko", "/lib/modules/4.9.191/dc-sunxi.ko"},
		ModuleDeps: map[string][]string{"dc_sunxi": {"pvrsrvkm"}, "bar": nil},
		Wifi:       true,
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("parseBoardEnv = %#v\nwant %#v", env, want)
	}
	if parseBoardEnv("MODULES=a.ko\n").ModuleDeps != nil {
		t.Fatal("no MODULE_DEPS line must leave the dependencies unknown")
	}

	words := []struct {
		in   string
		want []string
		ok   bool
	}{
		{`/usr/trimui/bin/trimui_inputd -d`, []string{"/usr/trimui/bin/trimui_inputd", "-d"}, true},
		{`wpa_supplicant -c '/etc/wifi/wpa supplicant.conf'`, []string{"wpa_supplicant", "-c", "/etc/wifi/wpa supplicant.conf"}, true},
		{`sh -c 'echo "$0"' 'a|b'`, []string{"sh", "-c", `echo "$0"`, "a|b"}, true},
		{`'it'"'"'s' ''`, []string{"it's", ""}, true},
		{`"a\"b" c\ d`, []string{`a"b`, "c d"}, true},
		{`echo $HOME`, nil, false},
		{`a; b`, nil, false},
		{`ls *.ko`, nil, false},
		{`'open`, nil, false},
		{`#comment`, nil, false},
	}
	for _, w := range words {
		got, ok := shellWords(w.in)
		if ok != w.ok || ok && !reflect.DeepEqual(got, w.want) {
			t.Errorf("shellWords(%q) = %q, %v; want %q, %v", w.in, got, ok, w.want, w.ok)
		}
	}

	daemons := parseDaemons("hardware|/usr/trimui/bin|/usr/trimui/lib|/usr/trimui/bin/trimui_inputd\n" +
		"radio|/||udhcpc -i wlan0 'a|b'\nbad line\nradio|/||\n")
	if len(daemons) != 2 || daemons[1].Cmd != "udhcpc -i wlan0 'a|b'" || !stickDaemon(daemons) {
		t.Fatalf("parseDaemons = %#v", daemons)
	}
	gpio := parseGPIO("110 out 1\n12 in\nbad-line x y\n243 out 1 extra\n\n7\n")
	wantGPIO := []gpioLine{{"110", "out", "1"}, {"12", "in", ""}, {"7", "", ""}}
	if !reflect.DeepEqual(gpio, wantGPIO) {
		t.Fatalf("parseGPIO = %#v", gpio)
	}

	s := defaultSettings(boardEnv{Wifi: true})
	s.apply("WIFI=0\r\nEARLY_START=0\nREADAHEAD_KB=0512\nBOOTER=2\nMODULES_PARALLEL=0\nREADAHEAD_KB=2048")
	if s != (bootSettings{Wifi: false, EarlyStart: false, ReadaheadKB: 2048, Booter: true, Parallel: false}) {
		t.Fatalf("boot.env settings = %+v", s)
	}
}

// The module plan: dependencies and per-class order kept, everything else side
// by side, display/GPU/input/sound first.
func TestModulePlanOrder(t *testing.T) {
	paths := []string{
		"/m/pvrsrvkm.ko",    // 0 gpu
		"/m/dc_sunxi.ko",    // 1 display, uses pvrsrvkm
		"/m/gpio-keys.ko",   // 2 input
		"/m/snd_soc_x.ko",   // 3 sound
		"/m/foo.ko",         // 4 other
		"/m/bar.ko",         // 5 other, uses foo
		"/m/sunxi_gpadc.ko", // 6 input, after gpio_keys
		"/m/pvrsrvkm.ko",    // a second line for one module: once
	}
	deps := map[string][]string{"dc_sunxi": {"pvrsrvkm"}, "bar": {"foo", "not_on_card"}}
	nodes, note := planModules(paths, deps, false)
	if note != "" || len(nodes) != 7 {
		t.Fatalf("plan: %d nodes, note %q", len(nodes), note)
	}
	needs := [][]int{nil, {0}, nil, nil, nil, {4}, {2}}
	for i, n := range nodes {
		if !reflect.DeepEqual(n.Needs, needs[i]) {
			t.Errorf("%s needs %v, want %v", n.Name, n.Needs, needs[i])
		}
	}
	if got, want := moduleWaves(nodes), [][]int{{0, 2, 3, 4}, {1, 5, 6}}; !reflect.DeepEqual(got, want) {
		t.Errorf("waves %v, want %v", got, want)
	}
	// One worker: the order is the preference among the ready ones.
	var order []int
	runModules(nodes, 1, func(i int) { order = append(order, i) })
	if want := []int{0, 1, 2, 6, 3, 4, 5}; !reflect.DeepEqual(order, want) {
		t.Errorf("one-worker order %v, want %v", order, want)
	}
	// Several workers: never a module before what it needs, never more at once.
	var mu sync.Mutex
	finished := map[int]bool{}
	running, most := 0, 0
	runModules(nodes, 3, func(i int) {
		mu.Lock()
		for _, j := range nodes[i].Needs {
			if !finished[j] {
				t.Errorf("%s started before %s", nodes[i].Name, nodes[j].Name)
			}
		}
		running++
		if running > most {
			most = running
		}
		mu.Unlock()
		mu.Lock()
		running--
		finished[i] = true
		mu.Unlock()
	})
	if len(finished) != len(nodes) || most > 3 {
		t.Errorf("%d of %d loaded, %d at once", len(finished), len(nodes), most)
	}

	// Infra first for everyone after it; a GPU after the displays before it.
	nodes, _ = planModules([]string{"/m/i2c_x.ko", "/m/disp.ko", "/m/mali.ko", "/m/axp2202.ko"}, map[string][]string{}, false)
	if got := [][]int{nodes[1].Needs, nodes[2].Needs, nodes[3].Needs}; !reflect.DeepEqual(got, [][]int{{0}, {0, 1}, {0}}) {
		t.Errorf("infra/GPU needs %v", got)
	}
	// Unknown dependencies, a dump that contradicts itself, or MODULES_PARALLEL=0:
	// the stock order, one after another.
	for _, c := range []struct {
		deps   map[string][]string
		serial bool
	}{{nil, false}, {map[string][]string{"a": {"b"}}, false}, {map[string][]string{}, true}} {
		nodes, note := planModules([]string{"/m/a.ko", "/m/b.ko", "/m/c.ko"}, c.deps, c.serial)
		if note == "" || !reflect.DeepEqual(moduleWaves(nodes), [][]int{{0}, {1}, {2}}) {
			t.Errorf("deps %v serial %v: note %q, waves %v", c.deps, c.serial, note, moduleWaves(nodes))
		}
	}
}

// A kernel log from a boot with initcall_debug (build_image.py --boot-profile).
func TestSlowInitcalls(t *testing.T) {
	log := "<7>[    0.101] calling  sunxi_disp_init+0x0/0x40 @ 1\n" +
		"<7>[    0.301] initcall sunxi_disp_init+0x0/0x40 returned 0 after 195312 usecs\n" +
		"<7>[    0.302] initcall fast_init+0x0/0x8 returned 0 after 12 usecs\n" +
		"[    0.900] initcall mmc_blk_init+0x0/0xf0 [mmc_block] returned 0 after 4500 usecs\n" +
		"[    1.000] blacklisting initcall foo\n" +
		"[    1.100] initcall broken returned 0 after x usecs\n"
	got := slowInitcalls(log, 2)
	want := []initcall{{"sunxi_disp_init", 195312}, {"mmc_blk_init", 4500}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slowInitcalls = %v, want %v", got, want)
	}
}
