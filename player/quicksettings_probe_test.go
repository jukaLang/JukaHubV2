package main

import (
	"errors"
	"testing"
	"time"
)

// A probe that read the host before a change made from the panel must not snap
// the row back: applied, it put the volume slider on the old level and the next
// step jumped from there. The stale answer is dropped and a fresh probe runs.
func TestQSProbeOlderThanAnEditIsDropped(t *testing.T) {
	prev, prevGen, prevPending, prevRerun := qs, qsEditGen, qsProbePending, qsProbeRerun
	prevRunner := systemCommandRunner
	// A channel of its own: earlier tests open Quick Settings, whose probes
	// fill the shared one-slot channel and are never drained, so a send to it
	// here would block for good.
	prevCh := qsProbeCh
	qsProbeCh = make(chan qsProbe, 1)
	// No host is touched: the re-run probe reads through this stub.
	systemCommandRunner = func(string, ...string) (string, error) { return "", errors.New("stubbed") }
	t.Cleanup(func() {
		if qsProbePending {
			// Let the re-run finish so its result cannot land in a later test.
			select {
			case <-qsProbeCh:
			case <-time.After(5 * time.Second):
			}
		}
		systemCommandRunner = prevRunner
		qsProbeCh = prevCh
		qs, qsEditGen, qsProbePending, qsProbeRerun = prev, prevGen, prevPending, prevRerun
	})

	qs.toggles = []qsToggle{{}, {}, {}}
	qs.sliders = []qsSlider{{Action: "volume", Available: true, Value: 0.7, Max: 1}, {Action: "brightness"}}
	qs.lastVolume = 70
	qsProbeRerun = false

	// The probe started at generation 4; the user moved the slider since.
	qsEditGen = 5
	qsProbePending = true
	qsProbeCh <- qsProbe{gen: 4, volume: 20, volumeOK: true}
	qsDrainProbe()
	if qs.sliders[0].Value != 0.7 || qs.lastVolume != 70 {
		t.Fatalf("a stale probe moved the slider to %.2f (last %d)", qs.sliders[0].Value, qs.lastVolume)
	}
	if !qsProbePending {
		t.Fatal("no fresh probe replaced the stale one")
	}
	select {
	case <-qsProbeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("the fresh probe never answered")
	}
	qsProbePending = false

	// A current probe is applied.
	qsProbePending = true
	qsProbeCh <- qsProbe{gen: qsEditGen, volume: 40, volumeOK: true}
	qsDrainProbe()
	if qs.sliders[0].Value != 0.4 || qs.lastVolume != 40 {
		t.Fatalf("a current probe was not applied: %.2f (last %d)", qs.sliders[0].Value, qs.lastVolume)
	}
	if qsProbePending {
		t.Fatal("a current probe started another one")
	}
}
