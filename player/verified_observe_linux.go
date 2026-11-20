//go:build linux

package main

// verified_observe_linux.go — the sound check of verified_observe.go: ALSA
// says, in /proc/asound/card*/pcm*p/sub*/status, which process opened each
// playback stream and whether it is running. A running stream opened by the
// game's process group (the child leads it: screenChildGroupConfigure) is the
// game playing sound. JukaHub's own output — music under the game, an alarm,
// the overlay — is JukaHub's process, not the game's group, and never counts.
// A game whose sound goes through a Bluetooth headset (bluealsa) or a shared
// mixer another process opened is not seen here, which is why not seeing a
// stream never records a fail.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// vfoSoundStatusGlob finds every playback substream's status file.
const vfoSoundStatusGlob = "/proc/asound/card*/pcm*p/sub*/status"

// vfoSoundSupported: Linux has the status files.
func vfoSoundSupported() bool { return true }

// vfoSoundSeen reports whether a running playback stream belongs to the child
// pid: to its process group when grouped (the child leads its own), else to
// the child or a process it started. A few small procfs reads; the session's
// goroutine.
func vfoSoundSeen(pid int, grouped bool) bool {
	if pid <= 0 {
		return false
	}
	paths, err := filepath.Glob(vfoSoundStatusGlob)
	if err != nil {
		return false
	}
	self := os.Getpid()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		owner, running := vfoParseSoundStatus(string(data))
		if !running || owner <= 0 || owner == self {
			continue
		}
		if grouped {
			if pgid, err := syscall.Getpgid(owner); err == nil && pgid == pid {
				return true
			}
		}
		if vfoDescendsFrom(owner, pid) {
			return true
		}
	}
	return false
}

// vfoParseSoundStatus reads a substream's status: its owner's pid (0 when not
// said) and whether its state is RUNNING. A closed substream says "closed".
func vfoParseSoundStatus(text string) (owner int, running bool) {
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "state":
			running = strings.TrimSpace(value) == "RUNNING"
		case "owner_pid":
			owner, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	return owner, running
}

// vfoDescendsFrom reports whether pid is root or was started by it, by the
// parent links in /proc (a few levels; a game's helpers are near).
func vfoDescendsFrom(pid, root int) bool {
	for i := 0; i < 16 && pid > 1; i++ {
		if pid == root {
			return true
		}
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		ppid := vfoParsePPID(string(data))
		if ppid <= 0 || ppid == pid {
			return false
		}
		pid = ppid
	}
	return false
}

// vfoParsePPID is the parent pid in a /proc/<pid>/stat line: the second
// field after the command name, whose ")" is the line's last (the name may
// hold spaces and brackets). 0 when the line does not parse.
func vfoParsePPID(s string) int {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}
