package main

// voicemic_test.go — the shared microphone's pure parts: which PCMs the
// kernel can record from, which mixer switches are a microphone's, the channel
// a mono microphone lands on, and the order devices are tried in.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The kernel's PCM list: only lines with a capture substream count, in order,
// and a malformed line is skipped rather than guessed at.
func TestMicParseProcPCM(t *testing.T) {
	text := "00-00: sun50iw10codec-xxx : : playback 1 : capture 1\n" +
		"01-00: sndhdmi : : playback 1\n" +
		"02-03: snddmic : dmic capture : capture 2\n" +
		"02-04: oddname capture : : playback 1 : capture 0\n" +
		"garbage line\n" +
		"x-0: bad : : capture 1\n"
	got := micParseProcPCM(text)
	want := []micPCM{{card: 0, dev: 0}, {card: 2, dev: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("micParseProcPCM = %+v, want %+v", got, want)
	}
	if p := got[1].plug(); p != "plughw:2,3" {
		t.Errorf("plug name = %q", p)
	}
	if cards := micCaptureCards(append(got, micPCM{card: 0, dev: 1})); !reflect.DeepEqual(cards, []int{0, 2}) {
		t.Errorf("micCaptureCards = %v, want [0 2]", cards)
	}
	if micParseProcPCM("") != nil {
		t.Error("an empty list must give no PCM")
	}
}

// The sun50iw10 codec's own listing: the capture-side microphone switches
// that are off are the ones to turn on; a playback path, a mute and a volume
// never are.
func TestMicParseAmixerContents(t *testing.T) {
	out := strings.Join([]string{
		"numid=5,iface=MIXER,name='MIC1 gain volume'",
		"  ; type=INTEGER,access=rw------,values=1,min=0,max=31,step=0",
		"  : values=31",
		"  | dBscale-min=-12.00dB,step=1.00dB,mute=0",
		"numid=12,iface=MIXER,name='ADCL Input MIC1 Boost Switch'",
		"  ; type=BOOLEAN,access=rw------,values=1",
		"  : values=off",
		"numid=13,iface=MIXER,name='ADCR Input MIC2 Boost Switch'",
		"  ; type=BOOLEAN,access=rw------,values=1",
		"  : values=on",
		"numid=20,iface=MIXER,name='Mic1 Capture Switch',index=1",
		"  ; type=BOOLEAN,access=rw------,values=2",
		"  : values=on,off",
	}, "\n")
	got := micParseAmixerContents(out)
	want := []micAmixerControl{
		{name: "MIC1 gain volume", integer: true, count: 1, max: 31, values: []string{"31"}},
		{name: "ADCL Input MIC1 Boost Switch", boolean: true, count: 1, values: []string{"off"}},
		{name: "ADCR Input MIC2 Boost Switch", boolean: true, count: 1, values: []string{"on"}, on: true},
		{name: "Mic1 Capture Switch", boolean: true, count: 2, values: []string{"on", "off"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("micParseAmixerContents =\n%+v\nwant\n%+v", got, want)
	}
}

// What is changed and what it is put back to: an off microphone switch goes
// on, a microphone level at the bottom of its range goes to two thirds, and a
// level anywhere else, a switch already on and a playback control stay.
func TestMicMixerChange(t *testing.T) {
	for _, c := range []struct {
		ctl           micAmixerControl
		value, before string
		ok            bool
	}{
		{micAmixerControl{name: "ADCL Input MIC1 Boost Switch", boolean: true, count: 1, values: []string{"off"}}, "on", "off", true},
		{micAmixerControl{name: "Mic1 Capture Switch", boolean: true, count: 2, values: []string{"on", "off"}}, "on,on", "on,off", true},
		{micAmixerControl{name: "ADCR Input MIC2 Boost Switch", boolean: true, count: 1, values: []string{"on"}, on: true}, "", "", false},
		{micAmixerControl{name: "Left Output Mixer MIC1 Switch", boolean: true, count: 1, values: []string{"off"}}, "", "", false},
		{micAmixerControl{name: "MIC1 gain volume", integer: true, count: 1, max: 31, values: []string{"0"}}, "20", "0", true},
		{micAmixerControl{name: "ADC volume", integer: true, count: 2, max: 255, values: []string{"0", "0"}}, "170,170", "0,0", true},
		{micAmixerControl{name: "MIC1 gain volume", integer: true, count: 1, max: 31, values: []string{"31"}}, "", "", false},
		{micAmixerControl{name: "DAC volume", integer: true, count: 2, max: 255, values: []string{"0", "0"}}, "", "", false},
		{micAmixerControl{name: "LINEOUT volume", integer: true, count: 1, max: 31, values: []string{"0"}}, "", "", false},
	} {
		value, before, ok := micMixerChange(c.ctl)
		if value != c.value || before != c.before || ok != c.ok {
			t.Errorf("micMixerChange(%q %v) = %q, %q, %v; want %q, %q, %v",
				c.ctl.name, c.ctl.values, value, before, ok, c.value, c.before, c.ok)
		}
	}
}

func TestMicMixerSwitchWanted(t *testing.T) {
	for name, want := range map[string]bool{
		"ADCL Input MIC1 Boost Switch":       true,
		"ADCR Input MIC2 Boost Switch":       true,
		"Mic1 Capture Switch":                true,
		"LADC input Mixer MIC1 boost Switch": true,
		"MIC1 Switch":                        true,
		"Left Output Mixer MIC1 Switch":      false,
		"Mic1 Playback Switch":               false,
		"MIC1 to Lineout Switch":             false,
		"Mic Mute Switch":                    false,
		"Headphone Switch":                   false,
		"HpSpeaker Switch":                   false,
		"MIC1 gain volume":                   false,
		"Mic Boost Volume":                   false,
	} {
		if got := micMixerSwitchWanted(name); got != want {
			t.Errorf("micMixerSwitchWanted(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMicRepeatValue(t *testing.T) {
	for _, c := range []struct {
		v     string
		count int
		want  string
	}{{"on", 0, "on"}, {"on", 1, "on"}, {"off", 2, "off,off"}, {"on", 20, "on,on,on,on,on,on,on,on"}} {
		if got := micRepeatValue(c.v, c.count); got != c.want {
			t.Errorf("micRepeatValue(%q, %d) = %q, want %q", c.v, c.count, got, c.want)
		}
	}
}

// micPrepareMixer, against a stub amixer: the off microphone switch is turned
// on (with its name as one argument, never a shell line), nothing else is
// touched, it runs once a run, and the exit path turns it off again.
func TestMicPrepareMixerTurnsOnOnlyTheMicInputs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the mixer steps run on Linux only")
	}
	dir := t.TempDir()
	pcm := filepath.Join(dir, "pcm")
	if err := os.WriteFile(pcm, []byte("00-00: sun50iw10codec : : playback 1 : capture 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := strings.Join([]string{
		"numid=12,iface=MIXER,name='ADCL Input MIC1 Boost Switch'",
		"  ; type=BOOLEAN,access=rw------,values=1",
		"  : values=off",
		"numid=13,iface=MIXER,name='ADCR Input MIC2 Boost Switch'",
		"  ; type=BOOLEAN,access=rw------,values=1",
		"  : values=on",
		"numid=21,iface=MIXER,name='Left Output Mixer MIC1 Switch'",
		"  ; type=BOOLEAN,access=rw------,values=1",
		"  : values=off",
	}, "\n")
	var calls [][]string
	oldRunner, oldHave, oldPath := systemCommandRunner, micHaveAmixer, micProcPCMPath
	micMixer.mu.Lock()
	oldPrepared, oldChanged := micMixer.prepared, micMixer.changed
	micMixer.prepared, micMixer.changed = false, nil
	micMixer.mu.Unlock()
	t.Cleanup(func() {
		systemCommandRunner, micHaveAmixer, micProcPCMPath = oldRunner, oldHave, oldPath
		micMixer.mu.Lock()
		micMixer.prepared, micMixer.changed = oldPrepared, oldChanged
		micMixer.mu.Unlock()
	})
	micProcPCMPath = pcm
	micHaveAmixer = func() bool { return true }
	systemCommandRunner = func(name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) == 3 && args[2] == "contents" {
			return contents, nil
		}
		return "", nil
	}

	micPrepareMixer()
	want := [][]string{
		{"amixer", "-c", "0", "contents"},
		{"amixer", "-q", "-c", "0", "cset", "name='ADCL Input MIC1 Boost Switch'", "on"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %q\nwant %q", calls, want)
	}
	calls = nil
	micPrepareMixer()
	if len(calls) != 0 {
		t.Fatalf("a second open ran amixer again: %q", calls)
	}
	micRestoreMixer()
	want = [][]string{{"amixer", "-q", "-c", "0", "cset", "name='ADCL Input MIC1 Boost Switch'", "off"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("restore calls = %q\nwant %q", calls, want)
	}
}

// micStereo builds interleaved 16-bit frames from two channels.
func micStereo(l, r []int16) []byte {
	b := make([]byte, 4*len(l))
	for i := range l {
		binary.LittleEndian.PutUint16(b[4*i:], uint16(l[i]))
		binary.LittleEndian.PutUint16(b[4*i+2:], uint16(r[i]))
	}
	return b
}

// micTone is n samples of a square wave of amplitude a.
func micTone(n int, a int16) []int16 {
	s := make([]int16, n)
	for i := range s {
		if i%16 < 8 {
			s[i] = a
		} else {
			s[i] = -a
		}
	}
	return s
}

// A mono microphone wired to one ADC input: the picker returns that channel at
// full level instead of an average at half, and settles on it once enough
// voice has been heard.
func TestMicPickerFollowsTheLoudChannel(t *testing.T) {
	const n = 1024
	voice, quiet := micTone(n, 8000), micTone(n, 20)
	dst := make([]byte, 2*n)

	var p micPicker
	if got := p.downmix(dst, micStereo(quiet, voice)); got != 2*n {
		t.Fatalf("downmix wrote %d bytes, want %d", got, 2*n)
	}
	if s := int16(binary.LittleEndian.Uint16(dst)); s != 8000 {
		t.Errorf("right-only voice: first sample %d, want 8000 (the right channel, not the average)", s)
	}
	for i := 0; p.mode == 0 && i < 20; i++ {
		p.downmix(dst, micStereo(quiet, voice))
	}
	if p.mode != micPickRight {
		t.Fatalf("after half a second of voice the picker is %d, want right (%d)", p.mode, micPickRight)
	}

	var both micPicker
	both.downmix(dst, micStereo(voice, micTone(n, 6000)))
	if s := int16(binary.LittleEndian.Uint16(dst)); s != 7000 {
		t.Errorf("voice on both channels: first sample %d, want the average 7000", s)
	}
	if got := both.downmix(dst[:10], micStereo(voice, voice)); got != 10 {
		t.Errorf("a short dst: wrote %d bytes, want 10", got)
	}
}

func TestMicHasSound(t *testing.T) {
	if micHasSound(micStereo(make([]int16, 64), make([]int16, 64))) {
		t.Error("exact zeros counted as sound")
	}
	if micHasSound(micStereo(micTone(64, 3), micTone(64, -2))) {
		t.Error("a few bits of dither counted as sound")
	}
	if !micHasSound(micStereo(make([]int16, 64), micTone(64, 40))) {
		t.Error("a quiet room's noise did not count as sound")
	}
}

// The device that last gave sound is tried first; the rest keep their order.
func TestMicPreferCandidate(t *testing.T) {
	def := micCandidate{kind: micKindSDLDefault}
	plug := micCandidate{kind: micKindALSA, name: "plughw:0,0"}
	named := micCandidate{kind: micKindSDLNamed, name: "audiocodec,"}
	cands := []micCandidate{def, plug, named}
	if got := micPreferCandidate(cands, named); !reflect.DeepEqual(got, []micCandidate{named, def, plug}) {
		t.Errorf("prefer named: %+v", got)
	}
	if got := micPreferCandidate(cands, def); !reflect.DeepEqual(got, cands) {
		t.Errorf("prefer the first: %+v", got)
	}
	other := micCandidate{kind: micKindArecord, name: "plughw:1,0"}
	if got := micPreferCandidate(cands, other); !reflect.DeepEqual(got, cands) {
		t.Errorf("prefer one not in the list: %+v", got)
	}
	if !reflect.DeepEqual(cands, []micCandidate{def, plug, named}) {
		t.Error("micPreferCandidate changed its input")
	}
}
