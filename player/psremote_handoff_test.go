package main

import (
	"strings"
	"testing"
)

// The stream's hand-off script must leave the firmware UI to a launch.sh that
// waits for it, and must never start a MainUI next to the one the firmware
// supervisor brings back — two MainUIs on /dev/fb0 were the glitchy screen
// after leaving JukaHub.
func TestPSHandoffScriptLeavesTheScreenToAWaitingLauncher(t *testing.T) {
	engine := psEngine{Stream: "/mnt/SDCARD/Apps/JukaHub/bin/chiaki-stream", Root: "/mnt/SDCARD/Apps/JukaHub"}
	waits := psHandoffScript(42, engine, "/tmp/jukahub-ps-1.conf", "PS5", true)
	old := psHandoffScript(42, engine, "/tmp/jukahub-ps-1.conf", "PS5", false)

	if !strings.Contains(waits, "LAUNCHER_WAITS=1\n") {
		t.Fatalf("a waiting launcher must be named in the script:\n%s", waits)
	}
	if !strings.Contains(old, "LAUNCHER_WAITS=0\n") {
		t.Fatalf("an older launcher must be named in the script:\n%s", old)
	}
	for name, script := range map[string]string{"waits": waits, "old": old} {
		// pidof can print several pids; quoted, kill got one bad argument.
		if strings.Contains(script, `kill "$UI_PID"`) || strings.Contains(script, `kill -9 "$UI_PID"`) {
			t.Errorf("%s: MainUI pids must be passed unquoted to kill", name)
		}
		// The old ending started a MainUI two seconds after resuming the
		// supervisor, which starts its own.
		if strings.Contains(script, "sleep 2\nif [ -z \"$(pidof MainUI") {
			t.Errorf("%s: the script still starts MainUI next to the supervisor's", name)
		}
		if !strings.Contains(script, `"$BIN" "$SESSION"`) {
			t.Errorf("%s: the stream helper is not run", name)
		}
	}
	// The supervisor handling sits behind the mode switch, never on the path a
	// waiting launcher takes.
	stop := strings.Index(waits, "pkill -STOP runtrimui.sh")
	guard := strings.Index(waits, `if [ "$LAUNCHER_WAITS" = "1" ]; then`)
	if stop < 0 || guard < 0 || stop < guard {
		t.Errorf("the supervisor is stopped outside the older-launcher branch")
	}
}

func TestPSLauncherWaitsForHandoffNeedsTheRunningLaunchersWord(t *testing.T) {
	t.Setenv(psScreenHandoffEnv, "")
	t.Setenv("JUKAHUB_LAUNCHED_BY", "launch.sh")
	if psLauncherWaitsForHandoff() {
		t.Fatal("no JUKAHUB_SCREEN_HANDOFF from the running launcher: no wait to rely on")
	}
	t.Setenv(psScreenHandoffEnv, "1")
	if !psLauncherWaitsForHandoff() {
		t.Fatal("a launch.sh that exports the protocol waits for the helper")
	}
	t.Setenv("JUKAHUB_LAUNCHED_BY", "")
	if !launchedByDeviceLauncher() && psLauncherWaitsForHandoff() {
		t.Fatal("not started by launch.sh: nobody waits")
	}
}
