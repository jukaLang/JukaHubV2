package main

import "strings"

// foreignScreenProcess is the small part of /proc that matters when another
// emulator takes the handheld's display without a JukaHub launch record.
type foreignScreenProcess struct {
	pid   int
	name  string
	state string
	// start is the process's start time (/proc/<pid>/stat field 22), so a
	// reused pid is never taken for the emulator it replaced.
	start string
}

// foreignGameDisplayOwner only trusts a known full-screen emulator that has a
// display device open. A background helper or a stale zombie must never hide
// JukaHub just because it happens to be running beside it.
//
// A stopped emulator (state T) counts only when it is keep: the owner this
// session last saw running (foreignscreen_linux.go). A sleep stops the game —
// JukaHub's own (powersleep.go) or the firmware's — and JukaHub must not draw
// Home over it meanwhile: the game's picture and Home then took turns on the
// panel as soon as the game ran again at the wake. One that was already
// stopped before JukaHub ever saw it run (a crash left it paused) must not
// hide JukaHub for good. A running emulator always wins over a kept one.
func foreignGameDisplayOwner(procs []foreignScreenProcess, heldNode func(int) string, keep foreignScreenProcess) (foreignScreenProcess, string) {
	var kept foreignScreenProcess
	keptNode := ""
	for _, p := range procs {
		if p.pid <= 0 || p.state == "Z" || !foreignGameProcessName(p.name) {
			continue
		}
		stopped := p.state == "T" || p.state == "t"
		if stopped && (keep.pid <= 0 || p.pid != keep.pid || p.start != keep.start || kept.pid != 0) {
			continue
		}
		node := heldNode(p.pid)
		if node == "" {
			continue
		}
		if !stopped {
			return p, node
		}
		kept, keptNode = p, node
	}
	return kept, keptNode
}

func foreignGameProcessName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "minarch", "minarch.elf", // NextUI / MinUI
		"ra32.trimui", "ra64.trimui", "retroarch", "retroarch64", // stock / CrossMix
		"ppsspp", "ppssppsdl", "drastic", "drastic.elf":
		return true
	}
	return false
}
