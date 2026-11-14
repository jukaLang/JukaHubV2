package main

import "strings"

// foreignScreenProcess is the small part of /proc that matters when another
// emulator takes the handheld's display without a JukaHub launch record.
type foreignScreenProcess struct {
	pid   int
	name  string
	state string
}

// foreignGameDisplayOwner only trusts a known full-screen emulator that has a
// display device open. A background helper or a stale zombie must never hide
// JukaHub just because it happens to be running beside it.
func foreignGameDisplayOwner(procs []foreignScreenProcess, heldNode func(int) string) (foreignScreenProcess, string) {
	for _, p := range procs {
		if p.pid <= 0 || p.state == "Z" || p.state == "T" || p.state == "t" || !foreignGameProcessName(p.name) {
			continue
		}
		if node := heldNode(p.pid); node != "" {
			return p, node
		}
	}
	return foreignScreenProcess{}, ""
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
