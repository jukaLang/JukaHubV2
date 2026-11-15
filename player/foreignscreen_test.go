package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestForeignGameDisplayOwner(t *testing.T) {
	procs := []foreignScreenProcess{
		{pid: 1, name: "JukaHub", state: "R"},
		{pid: 2, name: "minarch.elf", state: "Z"},
		{pid: 3, name: "minarch.elf", state: "S"},
		{pid: 4, name: "activities", state: "S"},
		{pid: 5, name: "ra64.trimui", state: "R"},
		{pid: 6, name: "minarch.elf", state: "T"},
	}
	held := map[int]string{1: "/dev/fb0", 2: "/dev/fb0", 4: "/dev/fb0", 5: "/dev/dri/card0", 6: "/dev/fb0"}
	lookup := func(pid int) string { return held[pid] }
	if got, node := foreignGameDisplayOwner(procs, lookup); got.pid != 5 || node != "/dev/dri/card0" {
		t.Fatalf("owner = %+v, %q; want the live RetroArch display", got, node)
	}
	held[3] = "/dev/fb0"
	if got, node := foreignGameDisplayOwner(procs, lookup); got.pid != 3 || node != "/dev/fb0" {
		t.Fatalf("owner = %+v, %q; want the first live emulator display", got, node)
	}
	delete(held, 3)
	delete(held, 5)
	if got, node := foreignGameDisplayOwner(procs, lookup); got.pid != 0 || node != "" {
		t.Fatalf("owner = %+v, %q; helpers and zombies must not take the screen", got, node)
	}
}

// "Close all apps & games" (closeall.go) reads /proc: a stat line whose name
// holds spaces and parentheses, kernel threads, and which processes of a
// handheld's table it would close.
func TestCloseAllProcScan(t *testing.T) {
	st, ok := closeAllParseStat([]byte("812 (ra64 (x) y) S 1 812 700 0 -1 4194560 10 0 0 0 5 3 0 0 20 0 4 0 12345 1000 50 0"))
	if !ok || st.comm != "ra64 (x) y" || st.state != 'S' || st.ppid != 1 || st.pgid != 812 || st.start != 12345 || st.kernel() {
		t.Fatalf("stat = %+v, %t", st, ok)
	}
	if _, ok := closeAllParseStat([]byte("9 (sh) S 1 9")); ok {
		t.Fatal("a short stat line parsed")
	}
	if kt, ok := closeAllParseStat([]byte("40 (kworker/0:1) I 2 0 0 0 -1 69238880 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0")); !ok || !kt.kernel() {
		t.Fatalf("kernel thread = %+v, %t", kt, ok)
	}
	if got := closeAllSplitCmdline([]byte("box64\x00wine64\x00game.exe\x00")); len(got) != 3 || got[2] != "game.exe" {
		t.Fatalf("cmdline = %q", got)
	}

	all := map[int]closeAllStat{
		1:   {pid: 1, pgid: 1, state: 'S', comm: "init"},
		2:   {pid: 2, state: 'S', comm: "kthreadd", flags: closeAllKThread},
		100: {pid: 100, ppid: 1, pgid: 100, state: 'S', comm: "sh"},        // launch.sh
		101: {pid: 101, ppid: 100, pgid: 100, state: 'S', comm: "JukaHub"}, // this process
		200: {pid: 200, ppid: 1, pgid: 200, state: 'S', comm: "ra64.trimui"},
		300: {pid: 300, ppid: 101, pgid: 300, state: 'T', comm: "sh"}, // the game on screen
		301: {pid: 301, ppid: 300, pgid: 300, state: 'T', comm: "PPSSPPSDL"},
		302: {pid: 302, ppid: 1, pgid: 300, state: 'S', comm: "gptokeyb"}, // reparented, same group
		400: {pid: 400, ppid: 101, pgid: 101, state: 'S', comm: "llama-server"},
		401: {pid: 401, ppid: 101, pgid: 101, state: 'S', comm: "juka-whisper"},
		500: {pid: 500, ppid: 1, pgid: 500, state: 'S', comm: "box64"},
		501: {pid: 501, ppid: 1, pgid: 501, state: 'S', comm: "notepad.exe"},
		600: {pid: 600, ppid: 1, pgid: 600, state: 'S', comm: "dropbear"},
		700: {pid: 700, ppid: 1, pgid: 700, state: 'Z', comm: "retroarch"},
		800: {pid: 800, ppid: 1, pgid: 800, state: 'S', comm: "helper"},
		801: {pid: 801, ppid: 1, pgid: 801, state: 'S', comm: "Celeste"},
		802: {pid: 802, ppid: 1, pgid: 802, state: 'S', comm: "drastic"},
	}
	read := func(pid int) ([]string, string) {
		switch pid {
		case 800:
			return []string{"/mnt/SDCARD/Apps/JukaHub/ports/helper"}, "/mnt/SDCARD/Apps/JukaHub/ports/helper"
		case 801:
			return []string{"./Celeste"}, "/mnt/SDCARD/Data/ports/celeste/Celeste"
		case 802:
			return nil, "" // exiting: no command line
		}
		return []string{all[pid].comm}, ""
	}
	roots := map[int]bool{300: true}
	never := map[int]bool{1: true, 100: true, 101: true}
	pick := func(keepWine bool) string {
		var b []string
		for _, p := range closeAllPick(all, roots, never, "/mnt/SDCARD/Apps/JukaHub", keepWine, read) {
			b = append(b, strconv.Itoa(p.pid)+"="+p.label)
		}
		return strings.Join(b, " ")
	}
	if got, want := pick(false), "200=RetroArch 500=Wine 501=Wine 801=Celeste"; got != want {
		t.Fatalf("picked %q, want %q", got, want)
	}
	if got, want := pick(true), "200=RetroArch 801=Celeste"; got != want {
		t.Fatalf("picked beside a Wine game %q, want %q", got, want)
	}
}
