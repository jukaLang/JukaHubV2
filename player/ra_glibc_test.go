package main

import (
	"strings"
	"testing"
)

// raGlibcSetupText's /dev/shm step: a firmware with no /dev/shm (CrossMix on the
// Smart Pro S mounts none) leaves a core that keeps its memory arena in a POSIX
// shared mapping without a place to put it — the buildbot's SwanStation build of
// 2026-11-28 stopped the game after a second with "[Create] shm_open failed: 2"
// and "Failed to allocate memory" (JH-HW3M9G). The game's own mount namespace
// can have one, so the step has to be there, and inside the namespace.
func TestRaGlibcSetupGivesTheGameItsOwnDevShm(t *testing.T) {
	script := raGlibcSetupText("/bin/mount", "/bin/mkdir", "/bin/readlink", "")
	if mount := "if '/bin/mount' -n -t tmpfs -o mode=1777 tmpfs /dev/shm 2>/dev/null; then"; !strings.Contains(script, mount) {
		t.Fatalf("the setup script does not make /dev/shm for the game:\n%s", script)
	}
	if !strings.Contains(script, "'/bin/mkdir' -p /dev/shm") {
		t.Errorf("the setup script cannot create the folder first:\n%s", script)
	}
	// After the namespace check: a mount made outside the game's own namespace
	// would change the firmware for everything else.
	nsAt := strings.Index(script, "the game has no mount namespace of its own")
	callAt := strings.Index(script, "\nrag_shm\n")
	if nsAt < 0 || callAt < 0 || callAt < nsAt {
		t.Errorf("/dev/shm is made outside the game's namespace (namespace check at %d, the step at %d)", nsAt, callAt)
	}
	if w := "if [ -d /dev/shm ] && [ -w /dev/shm ]; then"; !strings.Contains(script, w) {
		t.Errorf("the setup script does not keep a /dev/shm the firmware does have:\n%s", script)
	}

	// A firmware with no mkdir still gets a script that runs: the step falls
	// back to the firmware's own folder, and says so when even that fails.
	bare := raGlibcSetupText("/bin/mount", "", "/bin/readlink", "")
	if strings.Contains(bare, "-p /dev/shm") {
		t.Errorf("the setup script names an empty mkdir:\n%s", bare)
	}
	if !strings.Contains(bare, "could not be made") {
		t.Errorf("the setup script says nothing when /dev/shm cannot be made:\n%s", bare)
	}
}
