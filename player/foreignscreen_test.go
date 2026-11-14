package main

import "testing"

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
