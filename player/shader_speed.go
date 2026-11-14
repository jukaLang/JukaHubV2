package main

// A shader's speed report uses RetroArch's rendered frame counter during the
// actual game. It never estimates speed from a shader name or a menu benchmark.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type shaderSpeedResult struct {
	Mode      string    `json:"mode"`
	FPS       float64   `json:"fps,omitempty"`
	Target    float64   `json:"target,omitempty"`
	Frames    uint64    `json:"frames,omitempty"`
	Seconds   float64   `json:"seconds,omitempty"`
	At        time.Time `json:"at"`
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
}

var shaderSpeedPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "shader-speed.json")
}

var shaderSpeedStore struct {
	sync.Mutex
	loaded bool
	gen    uint64
	values map[string]shaderSpeedResult
}

func shaderSpeedLoadLocked() {
	if shaderSpeedStore.loaded {
		return
	}
	shaderSpeedStore.loaded = true
	shaderSpeedStore.values = map[string]shaderSpeedResult{}
	if path := shaderSpeedPath(); path != "" {
		b, err := os.ReadFile(path)
		if err == nil && len(b) <= 2<<20 {
			_ = json.Unmarshal(b, &shaderSpeedStore.values)
		}
	}
}

func shaderSpeedGet(rom, mode string) (shaderSpeedResult, bool) {
	shaderSpeedStore.Lock()
	defer shaderSpeedStore.Unlock()
	shaderSpeedLoadLocked()
	r, ok := shaderSpeedStore.values[rom]
	return r, ok && r.Mode == mode
}

func shaderSpeedSave(rom string, result shaderSpeedResult) {
	shaderSpeedStore.Lock()
	defer shaderSpeedStore.Unlock()
	shaderSpeedLoadLocked()
	shaderSpeedStore.values[rom] = result
	if path := shaderSpeedPath(); path != "" {
		if b, err := json.MarshalIndent(shaderSpeedStore.values, "", "  "); err == nil {
			_ = AtomicWrite(path, append(b, '\n'), 0o644)
		}
	}
}

func shaderSpeedTarget(rom string) float64 {
	name := strings.ToLower(filepath.Base(rom))
	if strings.Contains(name, "(europe)") || strings.Contains(name, "(pal)") || strings.Contains(name, "[pal]") {
		return 50
	}
	return 60
}

func shaderSpeedParseFrameCount(reply string) (uint64, bool) {
	fields := strings.Fields(reply)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "frame_count") {
		return 0, false
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	return n, err == nil
}

func shaderSpeedFastForward(reply string) bool {
	fields := strings.Fields(strings.ReplaceAll(reply, ":", " "))
	if len(fields) < 2 || !strings.EqualFold(fields[0], "fast_forward") {
		return false
	}
	switch strings.ToLower(fields[1]) {
	case "true", "1", "on", "yes":
		return true
	}
	return false
}

func shaderSpeedLabel(rom, mode string) (string, string) {
	if !videoFilterIsLook(mode) {
		return "Not a shader", "Choose a look and play to measure its speed"
	}
	r, ok := shaderSpeedGet(rom, mode)
	if !ok {
		return "Not measured", "Play with this look for 15 seconds to check speed"
	}
	if !r.Available {
		if r.Reason == "short" {
			return "Keep playing", "Play with this look for 15 seconds to check speed"
		}
		return "Unavailable", "No played-frame data was reported by RetroArch"
	}
	label := "Below full speed"
	if r.FPS >= r.Target*0.97 {
		label = "Full speed"
	}
	return label, fmt.Sprintf("%.1f of %.0f FPS over %.0f seconds of play", r.FPS, r.Target, r.Seconds)
}

// prepareShaderSpeedCheck runs after prepareVideoFilter confirmed a look was
// applied. It scopes RetroArch's network command port to this launch, then
// starts the counter watcher; unsupported builds show Unavailable in options.
func prepareShaderSpeedCheck(launcher, rom string) {
	mode := videoFilterForGame(rom)
	if !videoFilterIsLook(mode) || !retroArchLauncher(launcher) {
		return
	}
	port := 0
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if _, err := raScopeApply(path, []raScopeValue{{Key: "network_cmd_enable", Value: "true"}}); err != nil {
			continue
		}
		if raw, err := os.ReadFile(path); err == nil {
			port = networkCmdPort(string(raw))
			break
		}
	}
	if port > 0 {
		shaderSpeedStart(rom, mode, port)
	}
}

func shaderSpeedStart(rom, mode string, port int) {
	gen := shaderSpeedCancel()
	go shaderSpeedWatch(rom, mode, port, gen)
}

func shaderSpeedCancel() uint64 {
	shaderSpeedStore.Lock()
	shaderSpeedStore.gen++
	gen := shaderSpeedStore.gen
	shaderSpeedStore.Unlock()
	return gen
}

func shaderSpeedCurrent(gen uint64) bool {
	shaderSpeedStore.Lock()
	defer shaderSpeedStore.Unlock()
	return shaderSpeedStore.gen == gen
}

func shaderSpeedWatch(rom, mode string, port int, gen uint64) {
	const interval = 2 * time.Second
	const wait = 400 * time.Millisecond
	deadline := time.Now().Add(90 * time.Second)
	started := time.Now()
	var prevFrames uint64
	var prevAt time.Time
	var totalFrames uint64
	var totalSeconds float64
	seen := false
	for time.Now().Before(deadline) && shaderSpeedCurrent(gen) {
		time.Sleep(interval)
		if !shaderSpeedCurrent(gen) {
			return
		}
		status, err := askRetroArchNetCmd(port, "GET_STATUS", wait)
		if err != nil || raStatusWord(status) != "PLAYING" {
			if seen && !retroArchRunning() {
				break
			}
			prevAt = time.Time{}
			continue
		}
		if reply, err := askRetroArchNetCmd(port, "GET_OPTION fast_forward", wait); err == nil && shaderSpeedFastForward(reply) {
			prevAt = time.Time{}
			continue
		}
		reply, err := askRetroArchNetCmd(port, "GET_OPTION frame_count", wait)
		frames, ok := shaderSpeedParseFrameCount(reply)
		if err != nil || !ok {
			if time.Since(started) > 20*time.Second && !seen {
				break
			}
			continue
		}
		seen = true
		now := time.Now()
		if !prevAt.IsZero() && frames >= prevFrames {
			seconds := now.Sub(prevAt).Seconds()
			delta := frames - prevFrames
			fps := float64(delta) / seconds
			if seconds >= 1 && fps <= 120 && time.Since(started) > 7*time.Second {
				totalFrames += delta
				totalSeconds += seconds
			}
		}
		prevFrames, prevAt = frames, now
		if totalSeconds >= 15 {
			break
		}
	}
	if !shaderSpeedCurrent(gen) {
		return
	}
	result := shaderSpeedResult{Mode: mode, Target: shaderSpeedTarget(rom), At: time.Now(), Available: totalSeconds >= 10, Frames: totalFrames, Seconds: totalSeconds}
	if result.Available {
		result.FPS = float64(totalFrames) / totalSeconds
	} else if seen {
		result.Reason = "short"
	} else {
		result.Reason = "counter"
	}
	shaderSpeedSave(rom, result)
}
