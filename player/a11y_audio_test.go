package main

import (
	"encoding/binary"
	"testing"
)

func TestA11YMonoAndBalanceProcessStereoPCM(t *testing.T) {
	oldChannels, oldBalance := a11y.audioChannels, a11y.audioBalance
	defer func() { a11y.audioChannels, a11y.audioBalance = oldChannels, oldBalance }()

	frame := func(l, r int16) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint16(b[0:], uint16(l))
		binary.LittleEndian.PutUint16(b[2:], uint16(r))
		return b
	}
	a11y.audioChannels, a11y.audioBalance = audioChMono, 0
	pcm := a11yProcessAudio(frame(1000, 3000))
	if got := int16(binary.LittleEndian.Uint16(pcm[0:])); got != 2000 {
		t.Fatalf("mono left = %d, want 2000", got)
	}
	if got := int16(binary.LittleEndian.Uint16(pcm[2:])); got != 2000 {
		t.Fatalf("mono right = %d, want 2000", got)
	}

	a11y.audioChannels, a11y.audioBalance = audioChStereo, -100
	pcm = a11yProcessAudio(frame(1000, 3000))
	if got := int16(binary.LittleEndian.Uint16(pcm[0:])); got != 1000 {
		t.Fatalf("hard-left left = %d, want 1000", got)
	}
	if got := int16(binary.LittleEndian.Uint16(pcm[2:])); got != 0 {
		t.Fatalf("hard-left right = %d, want 0", got)
	}

	a11y.audioChannels, a11y.audioBalance = audioChStereo, 100
	pcm = a11yProcessAudio(frame(1000, 3000))
	if got := int16(binary.LittleEndian.Uint16(pcm[0:])); got != 0 {
		t.Fatalf("hard-right left = %d, want 0", got)
	}
	if got := int16(binary.LittleEndian.Uint16(pcm[2:])); got != 3000 {
		t.Fatalf("hard-right right = %d, want 3000", got)
	}

	// Mono left / right: the sum in one ear, silence in the other; on the
	// speaker both behave as Mono.
	if l, r := audioChannelsDownmix(audioChMonoLeft, 1000, 3000); l != 2000 || r != 0 {
		t.Fatalf("mono left = %d/%d, want 2000/0", l, r)
	}
	if l, r := audioChannelsDownmix(audioChMonoRight, 1000, 3000); l != 0 || r != 2000 {
		t.Fatalf("mono right = %d/%d, want 0/2000", l, r)
	}
	if got := audioChannelsEffective(audioChMonoRight, true); got != audioChMono {
		t.Fatalf("mono right on the speaker = %d, want Mono", got)
	}
	if got := audioChannelsEffective(audioChMonoRight, false); got != audioChMonoRight {
		t.Fatalf("mono right on headphones = %d, want Mono right", got)
	}
}

func TestA11YAudioRowsAndPersistence(t *testing.T) {
	labels := map[string]bool{}
	for _, row := range a11yRows() {
		labels[row.label] = true
	}
	for _, want := range []string{"Audio channels", "Audio balance", "Captions always on", "Caption size", "Caption background"} {
		if !labels[want] {
			t.Fatalf("Accessibility has no %q row", want)
		}
	}

	oldChannels, oldBalance := a11y.audioChannels, a11y.audioBalance
	oldAlways, oldSize, oldBackground := a11y.captionsAlways, a11y.captionSize, a11y.captionBackground
	defer func() {
		a11y.audioChannels, a11y.audioBalance = oldChannels, oldBalance
		a11y.captionsAlways, a11y.captionSize, a11y.captionBackground = oldAlways, oldSize, oldBackground
	}()
	a11y.audioChannels, a11y.audioBalance = audioChMono, -40
	a11y.captionsAlways, a11y.captionSize, a11y.captionBackground = true, 2, false
	cfg := &Config{}
	a11yStore(cfg)
	if cfg.Variables.Custom[a11yKeyMonoAudio] != true || cfg.Variables.Custom[a11yKeyAudioBalance] != -40 {
		t.Fatalf("audio settings not stored: %v", cfg.Variables.Custom)
	}
	if cfg.Variables.Custom[a11yKeyAudioChannels] != "mono" || audioChannelsFromConfig(cfg) != audioChMono {
		t.Fatalf("audio channels not stored: %v", cfg.Variables.Custom[a11yKeyAudioChannels])
	}
	// The old switch alone (an older version's config) reads as Mono; the new
	// key wins once it is there.
	old := &Config{}
	old.Variables.Custom = map[string]interface{}{a11yKeyMonoAudio: true}
	if got := audioChannelsFromConfig(old); got != audioChMono {
		t.Fatalf("a11y_mono_audio=true migrated to %d, want Mono", got)
	}
	old.Variables.Custom[a11yKeyAudioChannels] = "mono-left"
	if got := audioChannelsFromConfig(old); got != audioChMonoLeft {
		t.Fatalf("a11y_audio_channels=mono-left read as %d", got)
	}
	if cfg.Variables.Custom[a11yKeyCaptionsAlways] != true || cfg.Variables.Custom[a11yKeyCaptionSize] != 2 || cfg.Variables.Custom[a11yKeyCaptionBackground] != false {
		t.Fatalf("caption settings not stored: %v", cfg.Variables.Custom)
	}
}

func TestA11YCaptionsAlwaysStartEachVideo(t *testing.T) {
	old := a11y.captionsAlways
	defer func() { a11y.captionsAlways = old }()
	a11y.captionsAlways = true
	resetVideoSubtitles(&playbackSession{source: "/tmp/video.mp4", pageURL: "/tmp/video.mp4"})
	if !subtitlesEnabled() {
		t.Fatal("always-on captions did not start enabled")
	}
	resetVideoSubtitles(nil)
}

func TestA11YCaptionDefaultsAndLabels(t *testing.T) {
	if a11yCaptionSizeLabel() != "Medium" {
		t.Fatalf("default caption size = %q", a11yCaptionSizeLabel())
	}
	old := a11y.captionSize
	defer func() { a11y.captionSize = old }()
	a11y.captionSize = 2
	if a11yCaptionSizeLabel() != "Large" {
		t.Fatalf("large caption label = %q", a11yCaptionSizeLabel())
	}
}
