package main

// audiochannels.go — Accessibility → "Audio channels": Stereo, Mono, Mono
// left or Mono right. It replaced the single "Mono audio" switch (on = Mono).
//
// Mono left and Mono right put both channels, summed, into one ear and
// silence the other — for someone who hears on one side only, listening on
// headphones, earbuds, a Bluetooth headset or a USB DAC. On the built-in
// speaker they behave as Mono: the handheld's speakers sit a few centimetres
// apart, and one of them falling silent would only make it quieter.
//
// Two places apply it:
//
//   - JukaHub's own sound (video, music, the music under a game):
//     a11yProcessAudio (sound.go) on the stereo frames the mixer queues,
//     live, together with the balance.
//   - every game and app JukaHub starts: its ALSA "default" goes through an
//     ALSA route plugin whose ttable does the same mapping (jhchannels). It
//     wraps the definition musicgame.go writes for a launch — the mix the
//     music shares when music plays, else the card's own sysdefault or the
//     USB DAC — and the headset "default" of a Bluetooth launch
//     (audioChildALSAConfig, audioroute_bluealsa.go). A game reads its ALSA
//     setup when it starts, so a change applies from the next launch. In
//     Stereo nothing is added: the files are exactly what they were before.

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// a11yKeyAudioChannels is the setting: "stereo", "mono", "mono-left" or
	// "mono-right". Without it, a11y_mono_audio = true (older versions) reads
	// as Mono.
	a11yKeyAudioChannels = "a11y_audio_channels"
	// audioChannelsPCM is the route plugin's name in a child's ALSA files.
	audioChannelsPCM = "jhchannels"
)

const (
	audioChStereo = iota
	audioChMono
	audioChMonoLeft
	audioChMonoRight
	audioChCount
)

var audioChannelsValues = [audioChCount]string{"stereo", "mono", "mono-left", "mono-right"}
var audioChannelsLabels = [audioChCount]string{"Stereo", "Mono", "Mono left", "Mono right"}

// audioChannelsLive is the setting as a launch reads it (a launch may run on
// a goroutine, where a11y's fields are not read): published by
// audioChannelsPublish whenever the live setting changes, and once a second
// with the other sound settings (musicGameBind).
var audioChannelsLive atomic.Int32

// audioChannelsClamp keeps a value inside the four choices.
func audioChannelsClamp(ch int) int {
	if ch < 0 || ch >= audioChCount {
		return audioChStereo
	}
	return ch
}

// audioChannelsLabel is a choice's words.
func audioChannelsLabel(ch int) string {
	return audioChannelsLabels[audioChannelsClamp(ch)]
}

// audioChannelsParse reads a stored value; ok false for anything unknown.
func audioChannelsParse(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, v := range audioChannelsValues {
		if s == v {
			return i, true
		}
	}
	// A hand-edited number is accepted too.
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n < audioChCount {
		return n, true
	}
	return audioChStereo, false
}

// audioChannelsFromConfig is the saved choice, migrating the old switch.
func audioChannelsFromConfig(config *Config) int {
	if config == nil {
		return audioChStereo
	}
	if s, ok := customString(config.Variables.Custom, a11yKeyAudioChannels); ok {
		if ch, known := audioChannelsParse(s); known {
			return ch
		}
	}
	if a11yCustomBool(config, a11yKeyMonoAudio) {
		return audioChMono
	}
	return audioChStereo
}

// audioChannelsStore writes the choice, and the old switch beside it so an
// older version (or a profile saved by one) still finds mono on or off.
func audioChannelsStore(config *Config, ch int) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	ch = audioChannelsClamp(ch)
	config.Variables.Custom[a11yKeyAudioChannels] = audioChannelsValues[ch]
	config.Variables.Custom[a11yKeyMonoAudio] = ch != audioChStereo
}

// audioChannelsPublish makes ch what the next launch reads.
func audioChannelsPublish(ch int) {
	audioChannelsLive.Store(int32(audioChannelsClamp(ch)))
}

// audioOutputOnSpeaker reports whether JukaHub's sound goes to the built-in
// speaker: no USB DAC, Bluetooth headset or wired headphones named
// (probeAudioOutput keeps the name). Any goroutine; cheap.
func audioOutputOnSpeaker() bool {
	audioOutputState.mu.Lock()
	defer audioOutputState.mu.Unlock()
	return audioOutputState.name == ""
}

// audioChannelsEffective is the mapping that really applies: one ear only
// where there are ears to choose between.
func audioChannelsEffective(ch int, speaker bool) int {
	ch = audioChannelsClamp(ch)
	if speaker && (ch == audioChMonoLeft || ch == audioChMonoRight) {
		return audioChMono
	}
	return ch
}

// audioChannelsGameNow is the mapping a game launched now gets. Any goroutine.
func audioChannelsGameNow() int {
	return audioChannelsEffective(int(audioChannelsLive.Load()), audioOutputOnSpeaker())
}

// audioChannelsDownmix applies the mapping to one stereo frame (a11yProcessAudio).
func audioChannelsDownmix(ch int, l, r int16) (int16, int16) {
	if ch == audioChStereo {
		return l, r
	}
	m := int16((int32(l) + int32(r)) / 2)
	switch ch {
	case audioChMonoLeft:
		return m, 0
	case audioChMonoRight:
		return 0, m
	}
	return m, m
}

// audioChannelsRouteText is the ALSA route plugin name that plays into slave
// with the mapping ch, "" for Stereo (nothing is added then). The ttable is
// "client channel . slave channel  gain": both of the game's channels at half
// level into one or both of the card's; a card channel nothing feeds stays
// silent. The plug in front of it (jhgameout, or the headset "default",
// audioChannelsPlugText) converts whatever the game opens — mono, another
// rate or format — first.
func audioChannelsRouteText(name, slave string, ch int) string {
	var table string
	switch ch {
	case audioChMono:
		table = "\tttable.0.0 0.5\n\tttable.1.0 0.5\n\tttable.0.1 0.5\n\tttable.1.1 0.5\n"
	case audioChMonoLeft:
		table = "\tttable.0.0 0.5\n\tttable.1.0 0.5\n"
	case audioChMonoRight:
		table = "\tttable.0.1 0.5\n\tttable.1.1 0.5\n"
	default:
		return ""
	}
	return "pcm." + name + " {\n" +
		"\ttype route\n" +
		"\tslave {\n\t\tpcm \"" + slave + "\"\n\t\tchannels 2\n\t}\n" +
		table +
		"}\n"
}

// audioChannelsDefaultText is the headset "default" of a Bluetooth launch
// (audioChildALSAConfig): a plug onto the headset, through jhchannels when
// the setting asks for it.
func audioChannelsDefaultText(pcm string) string {
	if route := audioChannelsRouteText(audioChannelsPCM, pcm, audioChannelsGameNow()); route != "" {
		return route + audioChannelsPlugText("!default")
	}
	return "pcm.!default {\n\ttype plug\n\tslave.pcm \"" + pcm + "\"\n}\n"
}

// audioChannelsPlugText is the plug named name in front of jhchannels. It
// hands the route two channels: without "channels 2" the plug passes a mono
// program's one channel straight through (the route takes any count), and
// only one of the ttable's halves would reach the card — half the level. A
// mono program is copied into both first, so the halves add up again.
func audioChannelsPlugText(name string) string {
	return "pcm." + name + " {\n\ttype plug\n\tslave {\n\t\tpcm \"" + audioChannelsPCM + "\"\n\t\tchannels 2\n\t}\n}\n"
}

// audioChannelsPlanBuild is a launch's plan when no music plays (musicGamePlanNow):
// nil in Stereo — the launch stays untouched, as it always was — else the
// musicgame.go files with "default" through jhchannels into the card's own
// sysdefault or the USB DAC. musicWhy is why music that played at the launch
// cannot stay under it ("" when none played); it is said at the hand-off. A
// Bluetooth headset gets its route in its own file (audioChildALSAConfig),
// and a firmware that defines its own "default" is left alone: a plain
// sysdefault would skip whatever routing it does.
func audioChannelsPlanBuild(musicWhy string) *musicGamePlan {
	ch := audioChannelsGameNow()
	if ch == audioChStereo {
		return nil
	}
	base := spatialALSABaseConf()
	if base == "" {
		return nil
	}
	sysdefault := spatialALSAHasSysdefault(base)
	route := audioRouteChildPCM()
	slave := ""
	switch {
	case usbDACPCMIs(route):
		slave = route
	case route != "":
		return nil // Bluetooth: audioChildALSAConfig wraps the headset
	default:
		if !sysdefault {
			audioChannelsNoteOnce("this system's ALSA has no sysdefault")
			return nil
		}
		if f := spatialALSACustomDefault(); f != "" {
			audioChannelsNoteOnce("the firmware routes its sound itself (" + f + ")")
			return nil
		}
		slave = "sysdefault"
	}
	if !spatialALSAQuotable(slave) {
		return nil
	}
	p := &musicGamePlan{at: time.Now(), chanOnly: true, musicWhy: musicWhy, fallback: slave}
	if err := musicGameWriteFiles(p, slave, "", sysdefault); err != nil {
		logWarn("audio", "audio channels: could not write the game's sound setup: %v", err)
		return nil
	}
	return p
}

// a11yAudioChannelsSettingRow is Settings → Accessibility → Audio channels.
// Its ID stays "monoAudio", the switch it replaced, so the essentials list
// (setessentials.go) and anything pinned by ID still find it.
func a11yAudioChannelsSettingRow() setRow {
	return setRow{
		ID: "monoAudio", Label: "Audio channels", Kind: setKindChoice,
		Detail: func(*Config) string {
			switch a11y.audioChannels {
			case audioChMonoLeft, audioChMonoRight:
				return "One ear on headphones, Bluetooth or a USB DAC; both on the speaker. Games: from their next start"
			case audioChMono:
				return "Both channels mixed into each ear, for video, music and games (from their next start)"
			}
			return "Stereo, Mono, or Mono into one ear, for video, music and the games you start"
		},
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, audioChCount)
			for i := 0; i < audioChCount; i++ {
				out = append(out, setOption{Value: audioChannelsValues[i], Label: audioChannelsLabels[i]})
			}
			return out
		},
		Current: func(*Config) int { return audioChannelsClamp(a11y.audioChannels) },
		Choose: func(c *Config, value string) {
			ch, ok := audioChannelsParse(value)
			if !ok {
				return
			}
			if a11ySetAudioChannels(ch) {
				a11ySave(c)
			}
		},
	}
}

var audioChannelsNoted atomic.Bool

// audioChannelsNoteOnce logs, once a run, why games keep their stereo here.
func audioChannelsNoteOnce(why string) {
	if audioChannelsNoted.Swap(true) {
		return
	}
	logInfo("audio", "audio channels: games keep their own channels here: %s", why)
}
