//go:build linux

package main

// tts_cpu_linux.go — Piper kept off one core.
//
// ONNX Runtime, under Piper, starts a worker thread for every core and keeps
// them spinning between the voice's layers. On the Smart Pro's and the
// Brick's four A53 cores that left nothing for the interface, the "Hey Juka"
// listener and the sound thread while Juka spoke: frames dropped, the sound
// could crackle, and the voice itself came out no faster for the fight.
// Piper has no option for its thread count, so on the handheld it is pinned
// to every core but the first, which stays free for the rest. Its main
// thread is pinned as soon as it starts — the threads it makes while
// loading the voice inherit that — and every thread once more after
// ttsPiperPinAgain, in case one was made before.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const ttsPiperPinAgain = 3 * time.Second

// ttsPiperCPUSet is the cores Piper may use; false leaves it alone (not the
// handheld, or fewer than four cores to share).
func ttsPiperCPUSet() (unix.CPUSet, bool) {
	var set unix.CPUSet
	if !IsTrimuiDevice() {
		return set, false
	}
	if err := unix.SchedGetaffinity(0, &set); err != nil || set.Count() < 4 {
		return set, false
	}
	for cpu := 0; cpu < 1024; cpu++ {
		if set.IsSet(cpu) {
			set.Clear(cpu)
			break
		}
	}
	return set, true
}

// ttsPiperPinCPUs pins the Piper process pid (just started) to
// ttsPiperCPUSet. Failures change nothing: Piper then runs as before.
func ttsPiperPinCPUs(pid int) {
	set, ok := ttsPiperCPUSet()
	if !ok || pid <= 0 {
		return
	}
	if err := unix.SchedSetaffinity(pid, &set); err != nil {
		logDebug("tts", "Piper keeps every core: %v", err)
		return
	}
	go func() {
		time.Sleep(ttsPiperPinAgain)
		proc := filepath.Join("/proc", strconv.Itoa(pid))
		// The number may belong to another program once Piper has gone.
		comm, err := os.ReadFile(filepath.Join(proc, "comm"))
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(comm)), "piper") {
			return
		}
		tasks, err := os.ReadDir(filepath.Join(proc, "task"))
		if err != nil {
			return
		}
		for _, t := range tasks {
			if tid, err := strconv.Atoi(t.Name()); err == nil {
				_ = unix.SchedSetaffinity(tid, &set)
			}
		}
	}()
}
