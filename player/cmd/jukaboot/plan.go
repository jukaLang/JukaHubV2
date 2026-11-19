// jukaboot is JukaHub OS's own process 1 on the TrimUI Smart Pro SD image.
//
// The kernel starts it as init=/jukahub/jukaboot (os/smartpro/build_image.py
// sets that only when the package carries a static AArch64 jukaboot). It does
// what the shell process 1, os/smartpro/init, does — the same files, markers,
// boot.env keys, log lines and shutdown — with system calls instead of a fork
// per step, the kernel modules loaded side by side along their dependencies,
// and a timeline of every step written to the card's os.log. The shell init
// stays in the image: anything unexpected before JukaHub is running makes
// jukaboot hand the boot to it (exec /jukahub/init), and BOOTER=0 in the
// card's .userdata/jukahub/boot.env does the same on purpose, for an A/B.
//
// This file is the part that runs anywhere: the readers of the files
// build_image.py writes (board.env, daemons.list, gpio.list), boot.env, the
// module plan and its scheduler, and the initcall summary of a kernel log. The
// Linux-only booter is in the *_linux.go files.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ── board.env, boot.env, daemons.list, gpio.list ──────────────────────────

// shellWords splits s into words the way sh does for a simple command, for
// the quoting build_image.py writes with shlex.quote: bare words, '...' and
// "...", and backslashes. ok is false for anything only a real shell can
// expand: variables, command substitution, globs, redirections, ';', '&',
// '|', a comment or a tilde.
func shellWords(s string) (words []string, ok bool) {
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			closed := false
			for i++; i < len(s); i++ {
				d := s[i]
				if d == '"' {
					closed = true
					break
				}
				if d == '$' || d == '`' {
					return nil, false
				}
				if d == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
					i++
					if s[i] == '\n' {
						continue
					}
					d = s[i]
				}
				cur.WriteByte(d)
			}
			if !closed {
				return nil, false
			}
			inWord = true
		case c == '\\':
			if i+1 >= len(s) {
				return nil, false
			}
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
				inWord = true
			}
		case strings.IndexByte("$`;&|<>()*?[", c) >= 0:
			return nil, false
		case (c == '#' || c == '~') && !inWord:
			return nil, false
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, true
}

// envKey reports a plain variable name of board.env and boot.env.
func envKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// boardEnv is /jukahub/board.env: what build_image.py found the stock system
// running. A line that is missing or unreadable keeps the default, as it does
// when the shell init sources the file.
type boardEnv struct {
	DataName     string
	DataFSType   string
	Modules      []string // .ko paths, in the stock load order
	RadioModules []string
	// ModuleDeps names, for each module, the modules it needs loaded first
	// (the "used by" column of the stock /proc/modules and modules.dep). Nil
	// when board.env has no MODULE_DEPS line: then the modules load one after
	// another, in the stock order, as the shell init loads them.
	ModuleDeps map[string][]string
	Wifi       bool
}

func defaultBoardEnv() boardEnv {
	return boardEnv{DataName: "JUKAHUB", DataFSType: "vfat"}
}

// parseBoardEnv reads board.env: KEY=value lines, the value one shell word
// (build_image.py quotes it with shlex.quote).
func parseBoardEnv(text string) boardEnv {
	b := defaultBoardEnv()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		key, raw, found := strings.Cut(line, "=")
		if !found || !envKey(key) {
			continue
		}
		words, ok := shellWords(raw)
		if !ok || len(words) > 1 {
			continue
		}
		value := ""
		if len(words) == 1 {
			value = words[0]
		}
		switch key {
		case "DATA_NAME":
			if value != "" {
				b.DataName = value
			}
		case "DATA_FSTYPE":
			if value != "" {
				b.DataFSType = value
			}
		case "MODULES":
			b.Modules = splitList(value)
		case "RADIO_MODULES":
			b.RadioModules = splitList(value)
		case "MODULE_DEPS":
			b.ModuleDeps = parseModuleDeps(value)
		case "WIFI":
			b.Wifi = value == "1"
		}
	}
	return b
}

// splitList is a list value split as sh splits "for m in $LIST"; nil when empty.
func splitList(value string) []string {
	if f := strings.Fields(value); len(f) > 0 {
		return f
	}
	return nil
}

// parseModuleDeps reads MODULE_DEPS: "name:dep,dep name:dep ..." (module
// names, "-" read as "_"). Never nil: a present line means the dependencies
// are known, even when no module has any.
func parseModuleDeps(value string) map[string][]string {
	deps := map[string][]string{}
	for _, field := range strings.Fields(value) {
		name, list, found := strings.Cut(field, ":")
		name = normModule(name)
		if !found || name == "" {
			continue
		}
		var needs []string
		for _, d := range strings.Split(list, ",") {
			if d = normModule(d); d != "" && d != name {
				needs = append(needs, d)
			}
		}
		deps[name] = append(deps[name], needs...)
	}
	return deps
}

// bootSettings are the switches of a boot: board.env's Wi-Fi, then the card's
// boot.env (written by JukaHub, or by hand).
type bootSettings struct {
	Wifi        bool // WIFI=0|1: start Wi-Fi after JukaHub's first frame
	EarlyStart  bool // EARLY_START=0: JukaHub starts after the drivers
	ReadaheadKB int  // READAHEAD_KB=0..9999: the card disk's read-ahead; 0 leaves the kernel's
	Booter      bool // BOOTER=0: the shell process 1 (/jukahub/init) runs this boot
	Parallel    bool // MODULES_PARALLEL=0: the modules load one after another, in the stock order
}

func defaultSettings(board boardEnv) bootSettings {
	return bootSettings{Wifi: board.Wifi, EarlyStart: true, ReadaheadKB: 1024, Booter: true, Parallel: true}
}

// apply reads boot.env with the shell init's rules: KEY=VALUE per line, a
// trailing CR dropped (the file may have been edited on Windows), only the
// values each key allows; anything else is ignored.
func (s *bootSettings) apply(text string) {
	for _, line := range strings.Split(text, "\n") {
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSuffix(key, "\r")
		value = strings.TrimSuffix(value, "\r")
		bit := value == "0" || value == "1"
		switch key {
		case "WIFI":
			if bit {
				s.Wifi = value == "1"
			}
		case "EARLY_START":
			if bit {
				s.EarlyStart = value == "1"
			}
		case "BOOTER":
			if bit {
				s.Booter = value == "1"
			}
		case "MODULES_PARALLEL":
			if bit {
				s.Parallel = value == "1"
			}
		case "READAHEAD_KB":
			if value == "0" {
				s.ReadaheadKB = 0
			} else if plainNumber(value, 4) {
				s.ReadaheadKB, _ = strconv.Atoi(value)
			}
		}
	}
}

// plainNumber reports 1 to max digits without a leading zero: the numbers the
// shell scripts accept (a leading 0 would be octal to sh's arithmetic).
func plainNumber(s string, max int) bool {
	if s == "" || len(s) > max || s[0] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// daemon is a line of daemons.list: "group|folder|LD_LIBRARY_PATH|command",
// the command quoted for sh (build_image.py daemon_plan).
type daemon struct {
	Group, Dir, LD, Cmd string
}

func parseDaemons(text string) []daemon {
	var out []daemon
	for _, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 || strings.TrimSpace(parts[3]) == "" {
			continue
		}
		out = append(out, daemon{Group: parts[0], Dir: parts[1], LD: parts[2], Cmd: parts[3]})
	}
	return out
}

// stickDaemon reports whether the hardware group starts the stock stick
// daemon, whose pad the display-ready marker waits for (the shell's
// "grep '^hardware|.*trimui_inputd'").
func stickDaemon(list []daemon) bool {
	for _, d := range list {
		if d.Group == "hardware" && strings.Contains(d.Dir+"|"+d.LD+"|"+d.Cmd, "trimui_inputd") {
			return true
		}
	}
	return false
}

// gpioLine is a line of gpio.list: "number direction value".
type gpioLine struct {
	Num, Dir, Value string
}

// parseGPIO keeps the lines the shell init would replay: up to three
// fields, every character a digit, a small letter or the separator, and a
// number first.
func parseGPIO(text string) []gpioLine {
	var out []gpioLine
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || len(f) > 3 {
			continue
		}
		for len(f) < 3 {
			f = append(f, "")
		}
		ok := true
		for _, c := range f[0] + f[1] + f[2] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, gpioLine{Num: f[0], Dir: f[1], Value: f[2]})
		}
	}
	return out
}

// ── the module plan ────────────────────────────────────────────────────────

// Module classes, in the order the scheduler prefers among modules that are
// ready: buses and regulators everything may need, then what JukaHub opens
// first (display, GPU, input, sound), then the rest.
const (
	classInfra = iota
	classDisplay
	classGPU
	classInput
	classSound
	classOther
)

var classNames = [...]string{"infra", "display", "gpu", "input", "sound", "other"}

// classPatterns are matched against a module's name, first match wins (GPU
// before display: "pvr" names no display; input before sound: "adc" keys).
var classPatterns = []struct {
	class int
	parts []string
}{
	{classInfra, []string{"i2c", "spi", "regulator", "axp", "pmic", "pinctrl", "clk", "dma", "cma", "rsb", "twi", "pwm", "gpio_ex", "pca95", "mfd"}},
	{classGPU, []string{"pvr", "mali", "gpu", "sgx", "rogue", "img_"}},
	{classInput, []string{"input", "joy", "key", "adc", "touch", "gt9", "evdev", "hid", "gamepad", "rumble", "vibr", "haptic"}},
	{classSound, []string{"snd", "sound", "audio", "codec", "i2s", "dmic", "spdif", "ahub", "pcm", "amp"}},
	{classDisplay, []string{"disp", "lcd", "panel", "fb", "drm", "dc_", "tcon", "hdmi", "mipi", "dsi", "lvds", "backlight", "g2d", "edp"}},
}

func normModule(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), "-", "_")
}

// moduleName is a .ko path's module name, as /proc/modules writes it.
func moduleName(path string) string {
	base := path[strings.LastIndexByte(path, '/')+1:]
	return normModule(strings.TrimSuffix(base, ".ko"))
}

func moduleClass(name string) int {
	// ION, the buffers the GPU and display share: a word of its own ("ion"
	// inside a name is too common).
	for _, word := range strings.Split(name, "_") {
		if word == "ion" {
			return classInfra
		}
	}
	for _, p := range classPatterns {
		for _, part := range p.parts {
			if strings.Contains(name, part) {
				return p.class
			}
		}
	}
	return classOther
}

// modNode is one module of the plan.
type modNode struct {
	Path  string
	Name  string
	Class int
	Needs []int // earlier nodes that must be loaded first
}

// planModules orders paths (the stock load order) for loading side by side.
// Each module waits for the modules it uses (deps), for the module before it
// of its own class (so the kernel numbers input devices, sound cards and
// framebuffers as it did at the stock boot), for every infra module before it,
// and a GPU module for every display module before it. Every edge points to
// an earlier module, so the plan cannot deadlock. With serial set, deps
// unknown (nil), or a module that needs one the stock system loaded after it
// (the dump contradicts itself), each waits for the one before it: the shell
// init's order. note says why, for the log.
func planModules(paths []string, deps map[string][]string, serial bool) (nodes []modNode, note string) {
	index := map[string]int{}
	for _, p := range paths {
		name := moduleName(p)
		if name == "" {
			continue
		}
		if _, dup := index[name]; dup {
			continue
		}
		index[name] = len(nodes)
		nodes = append(nodes, modNode{Path: p, Name: name, Class: moduleClass(name)})
	}
	chain := func(why string) ([]modNode, string) {
		for i := range nodes {
			nodes[i].Needs = nil
			if i > 0 {
				nodes[i].Needs = []int{i - 1}
			}
		}
		return nodes, why
	}
	switch {
	case serial:
		return chain("one after another (MODULES_PARALLEL=0)")
	case deps == nil:
		return chain("one after another (board.env names no dependencies)")
	}
	lastOf := map[int]int{}
	var infra, display []int
	for i := range nodes {
		n := &nodes[i]
		need := map[int]bool{}
		for _, d := range deps[n.Name] {
			j, ok := index[normModule(d)]
			if !ok || j == i {
				continue // built in, a radio module or not on the system
			}
			if j > i {
				return chain(fmt.Sprintf("one after another (%s needs %s, which the stock system loaded after it)", n.Name, nodes[j].Name))
			}
			need[j] = true
		}
		if j, ok := lastOf[n.Class]; ok {
			need[j] = true
		}
		if n.Class != classInfra {
			for _, j := range infra {
				need[j] = true
			}
		}
		if n.Class == classGPU {
			for _, j := range display {
				need[j] = true
			}
		}
		for j := range need {
			n.Needs = append(n.Needs, j)
		}
		sort.Ints(n.Needs)
		lastOf[n.Class] = i
		switch n.Class {
		case classInfra:
			infra = append(infra, i)
		case classDisplay:
			display = append(display, i)
		}
	}
	return nodes, ""
}

// moduleWaves groups the nodes by the earliest round each could start in if
// every load took as long: the plan's shape, for the log and the tests.
func moduleWaves(nodes []modNode) [][]int {
	level := make([]int, len(nodes))
	var waves [][]int
	for i, n := range nodes {
		for _, j := range n.Needs {
			if j < i && level[j]+1 > level[i] {
				level[i] = level[j] + 1
			}
		}
		for len(waves) <= level[i] {
			waves = append(waves, nil)
		}
		waves[level[i]] = append(waves[level[i]], i)
	}
	return waves
}

// runModules calls load for every node once all its Needs have returned, at
// most workers at a time, preferring the lowest class (then the stock order)
// among the nodes that are ready. A load that panics counts as done. It
// returns when every node is done.
func runModules(nodes []modNode, workers int, load func(i int)) {
	if workers < 1 {
		workers = 1
	}
	waiting := make([]int, len(nodes))
	users := make([][]int, len(nodes))
	var ready []int
	for i, n := range nodes {
		for _, j := range n.Needs {
			if j >= 0 && j < i {
				waiting[i]++
				users[j] = append(users[j], i)
			}
		}
		if waiting[i] == 0 {
			ready = append(ready, i)
		}
	}
	done := make(chan int, len(nodes))
	running, finished := 0, 0
	for finished < len(nodes) {
		for running < workers && len(ready) > 0 {
			best := 0
			for k, i := range ready {
				b := ready[best]
				if nodes[i].Class < nodes[b].Class || nodes[i].Class == nodes[b].Class && i < b {
					best = k
				}
			}
			i := ready[best]
			ready = append(ready[:best], ready[best+1:]...)
			running++
			go func(i int) {
				defer func() {
					_ = recover()
					done <- i
				}()
				load(i)
			}(i)
		}
		if running == 0 {
			return // nothing ready, nothing running: cannot happen, the needs point backwards
		}
		i := <-done
		running--
		finished++
		for _, u := range users[i] {
			if waiting[u]--; waiting[u] == 0 {
				ready = append(ready, u)
			}
		}
	}
}

// ── the kernel log ─────────────────────────────────────────────────────────

// initcall is one "initcall NAME+0x0/0x0 returned R after N usecs" line of a
// kernel booted with initcall_debug.
type initcall struct {
	Name  string
	Usecs int64
}

// slowInitcalls returns the n slowest initcalls in a kernel log (dmesg text),
// slowest first: what --initcall-blacklist could skip.
func slowInitcalls(text string, n int) []initcall {
	var calls []initcall
	for _, line := range strings.Split(text, "\n") {
		at := strings.Index(line, "initcall ")
		if at < 0 {
			continue
		}
		rest := line[at+len("initcall "):]
		name, rest, found := strings.Cut(rest, " returned ")
		if !found {
			continue
		}
		_, rest, found = strings.Cut(rest, " after ")
		if !found {
			continue
		}
		num, unit, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if !strings.HasPrefix(unit, "usecs") {
			continue
		}
		us, err := strconv.ParseInt(num, 10, 64)
		if err != nil || us < 0 {
			continue
		}
		if plus := strings.IndexByte(name, '+'); plus > 0 {
			name = name[:plus]
		}
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		calls = append(calls, initcall{Name: name, Usecs: us})
	}
	sort.SliceStable(calls, func(a, b int) bool { return calls[a].Usecs > calls[b].Usecs })
	if len(calls) > n {
		calls = calls[:n]
	}
	return calls
}

// ── the tool (not process 1) ───────────────────────────────────────────────

var toolOut io.Writer = os.Stdout

// runTool is jukaboot started by hand: it prints what a boot would do, or
// the slowest kernel start-up calls of a saved kernel log.
//
//	jukaboot plan [DIR]        board.env/daemons.list/gpio.list in DIR (/jukahub)
//	jukaboot initcalls FILE    the 25 slowest initcalls in a boot-dmesg.txt
func runTool(args []string) int {
	if len(args) >= 1 && args[0] == "plan" && len(args) <= 2 {
		dir := "/jukahub"
		if len(args) == 2 {
			dir = args[1]
		}
		return printPlan(dir)
	}
	if len(args) == 2 && args[0] == "initcalls" {
		data, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "jukaboot:", err)
			return 1
		}
		for _, c := range slowInitcalls(string(data), 25) {
			fmt.Fprintf(toolOut, "%8.1f ms  %s\n", float64(c.Usecs)/1000, c.Name)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "jukaboot is JukaHub OS's process 1 (init=/jukahub/jukaboot).\n"+
		"usage: jukaboot plan [DIR] | jukaboot initcalls FILE")
	return 2
}

func printPlan(dir string) int {
	data, err := os.ReadFile(dir + "/board.env")
	if err != nil {
		fmt.Fprintln(os.Stderr, "jukaboot:", err)
		return 1
	}
	board := parseBoardEnv(string(data))
	fmt.Fprintf(toolOut, "card partition %s (%s), Wi-Fi %v\n", board.DataName, board.DataFSType, board.Wifi)
	nodes, note := planModules(board.Modules, board.ModuleDeps, false)
	if note != "" {
		fmt.Fprintln(toolOut, "modules:", note)
	}
	for w, wave := range moduleWaves(nodes) {
		var names []string
		for _, i := range wave {
			names = append(names, nodes[i].Name+" ("+classNames[nodes[i].Class]+")")
		}
		fmt.Fprintf(toolOut, "  wave %d: %s\n", w+1, strings.Join(names, ", "))
	}
	if len(board.RadioModules) > 0 {
		fmt.Fprintln(toolOut, "after JukaHub's first frame:", strings.Join(board.RadioModules, " "))
	}
	if text, err := os.ReadFile(dir + "/daemons.list"); err == nil {
		for _, d := range parseDaemons(string(text)) {
			argv, ok := shellWords(d.Cmd)
			how := "exec"
			if !ok {
				how = "sh -c"
			}
			fmt.Fprintf(toolOut, "daemon %s: %s (%s, %d words)\n", d.Group, d.Cmd, how, len(argv))
		}
	}
	if text, err := os.ReadFile(dir + "/gpio.list"); err == nil {
		fmt.Fprintf(toolOut, "%d GPIO lines\n", len(parseGPIO(string(text))))
	}
	return 0
}
