package main

// bt_audiofollow.go — the sound follows the headset, the way a phone does it.
//
// audioroute.go already moves every JukaHub output to a Bluetooth headset when
// one connects and back to the speaker when it goes. Three phone habits were
// missing, and live here:
//
//   - Each output keeps its own volume. The speaker and every headset have
//     their own last level (bt-output-volume.json in the data folder); moving
//     to an output sets its level, so a headset turned up loud does not leave
//     the speaker blaring when it drops, and the next connect is as loud as the
//     last one was.
//   - A headset that disconnects pauses the video and the music, so they do
//     not carry on out loud from the speaker. A stream that only hiccuped
//     (BlueZ still lists the headset) pauses nothing.
//   - The headset's codec is read once per connection, for the Bluetooth
//     page (SBC, AAC, aptX…), with what it means for delay.
//
// audioRouteUpdate calls btFollowRoute on the output monitor's goroutine after
// every route decision; everything that touches the UI or the volume worker is
// posted to the main loop (tbxPost). Handhelds only, like the route itself.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// btFollowSpeakerKey is the speaker's entry in the volume table.
const btFollowSpeakerKey = "speaker"

// btFollow is the route the sound was last on and the per-output levels.
var btFollow struct {
	mu sync.Mutex
	// key is the headset address the route is on, "" for the speaker.
	key    string
	loaded bool
	levels map[string]int
	// codec is each headset's codec, as BlueALSA last reported it.
	codec map[string]string
}

// btFollowVolumePath is the per-output volume table; "" without a data
// folder (nothing is remembered then).
func btFollowVolumePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "bt-output-volume.json")
}

// btFollowLoadLocked reads the table once per run.
func btFollowLoadLocked() {
	if btFollow.loaded {
		return
	}
	btFollow.loaded = true
	btFollow.levels = map[string]int{}
	path := btFollowVolumePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved map[string]int
	if json.Unmarshal(raw, &saved) != nil {
		return
	}
	for k, v := range saved {
		if (k == btFollowSpeakerKey || btNormMAC(k) != "") && v >= 0 && v <= 100 {
			btFollow.levels[k] = v
		}
	}
}

// btFollowEncodeLocked is the table as written to disk; nil when it cannot be
// encoded. The write itself (btFollowWrite) happens after the lock is let go:
// the Bluetooth page reads btFollow every frame (btHeadsetCodec).
func btFollowEncodeLocked() []byte {
	raw, err := json.Marshal(btFollow.levels)
	if err != nil {
		return nil
	}
	return raw
}

// btFollowWrite writes the table. A failure only costs the memory. Only the
// output monitor's goroutine writes it, so writes never cross.
func btFollowWrite(raw []byte) {
	path := btFollowVolumePath()
	if path == "" || raw == nil {
		return
	}
	if err := audioWriteFileAtomic(path, string(raw)); err != nil {
		logDebug("audio", "per-output volume not saved: %v", err)
	}
}

// btFollowKey is an output's entry in the table.
func btFollowKey(addr string) string {
	if addr == "" {
		return btFollowSpeakerKey
	}
	return btNormMAC(addr)
}

// btFollowRoute applies the phone habits after a route decision: addr is the
// headset BlueZ lists as connected ("" for none), routed whether JukaHub's
// sound is on it now. While a game holds the screen the game owns the sound
// and nothing changes here.
func btFollowRoute(addr string, routed bool) {
	if gameScreenSuspended() {
		return
	}
	now := ""
	if routed {
		now = btNormMAC(addr)
	}
	btFollow.mu.Lock()
	prev := btFollow.key
	btFollow.mu.Unlock()
	if now == prev {
		return
	}
	// The level the output being left was at (the cached reading: no mixer
	// process on this path).
	pct, known := volumeCurrent()
	btFollow.mu.Lock()
	if btFollow.key != prev {
		// Another decision got here first.
		btFollow.mu.Unlock()
		return
	}
	btFollow.key = now
	btFollowLoadLocked()
	if known {
		btFollow.levels[btFollowKey(prev)] = volumeClamp(pct)
	}
	next, have := btFollow.levels[btFollowKey(now)]
	raw := btFollowEncodeLocked()
	btFollow.mu.Unlock()
	btFollowWrite(raw)

	if now != "" {
		logInfo("audio", "sound follows %s", bzShortMAC(now))
		go btReadHeadsetCodec(now)
	}
	if have && (!known || next != pct) {
		tbxPost(func() {
			if appConfig != nil {
				volumeSetLevel(appConfig, next)
			}
		})
	}
	// The headset left (BlueZ no longer lists one) while JukaHub's sound was
	// on it: pause what plays, like a phone.
	if prev != "" && now == "" && btNormMAC(addr) == "" {
		tbxPost(btFollowPauseMedia)
	}
}

// btFollowPauseMedia pauses the video and the music that are playing, and
// says why. Main loop.
func btFollowPauseMedia() {
	paused := false
	if playerPhase() == "playing" {
		ToggleVideoPlayback()
		paused = true
	}
	if _, _, _, playing, isPaused := ambientNowPlaying(); playing && !isPaused {
		musicTogglePause()
		paused = true
	}
	if paused {
		ShowToast("Headphones disconnected — playback paused", ToastKindInfo)
	}
}

// btHeadsetCodecGate keeps one codec read at a time.
var btHeadsetCodecGate = make(chan struct{}, 1)

// btReadHeadsetCodec asks BlueALSA which codec addr's stream uses and keeps
// the answer for the Bluetooth page. The stream may need a moment after the
// route opens, so it asks twice at most.
func btReadHeadsetCodec(addr string) {
	select {
	case btHeadsetCodecGate <- struct{}{}:
	default:
		return
	}
	defer func() { <-btHeadsetCodecGate }()
	if !btToolExists("dbus-send") {
		return
	}
	for try := 0; try < 2; try++ {
		if try > 0 {
			time.Sleep(3 * time.Second)
		}
		out, err := btToolRun("dbus-send", "--system", "--print-reply", "--reply-timeout=2000",
			"--dest="+bluealsaService(), "/org/bluealsa", "org.freedesktop.DBus.ObjectManager.GetManagedObjects")
		if err != nil {
			out, err = btToolRun("dbus-send", "--system", "--print-reply", "--reply-timeout=2000",
				"--dest="+bluealsaService(), "/org/bluealsa", "org.bluealsa.Manager1.GetPCMs")
		}
		if err != nil {
			continue
		}
		if codec := parseBluealsaCodec(out, addr); codec != "" {
			btFollow.mu.Lock()
			if btFollow.codec == nil {
				btFollow.codec = map[string]string{}
			}
			btFollow.codec[btNormMAC(addr)] = codec
			btFollow.mu.Unlock()
			return
		}
	}
}

// btHeadsetCodec is the codec last read for a headset, "" when unknown.
func btHeadsetCodec(addr string) string {
	btFollow.mu.Lock()
	defer btFollow.mu.Unlock()
	return btFollow.codec[btNormMAC(addr)]
}
