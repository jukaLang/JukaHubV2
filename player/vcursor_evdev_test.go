package main

import (
	"testing"
	"time"
)

func TestWineStartTogglesKeyboardWithoutChangingQuitChord(t *testing.T) {
	now := time.Unix(100, 0)
	bridge := mbridgeEngine{p: mbridgeParams{keys: true, keyboardToggle: true}}
	bridge.setOn(true, now)
	events, _ := bridge.feed(evdevEvent{Type: evdevEvKey, Code: evdevBtnStart, Value: 1}, now.Add(time.Second), nil)
	if len(events) != 3 || events[0] != (evdevEvent{Type: evdevEvKey, Code: evdevKeyF12, Value: 1}) ||
		events[1] != (evdevEvent{Type: evdevEvSyn, Code: evdevSynReport}) ||
		events[2] != (evdevEvent{Type: evdevEvKey, Code: evdevKeyF12, Value: 0}) {
		t.Fatalf("Start press = %#v; want one F12 tap in separate frames", events)
	}
	events, _ = bridge.feed(evdevEvent{Type: evdevEvKey, Code: evdevBtnStart, Value: 1}, now.Add(1050*time.Millisecond), nil)
	if len(events) != 0 {
		t.Fatalf("repeated Start down = %#v; want no second tap", events)
	}
	events, _ = bridge.feed(evdevEvent{Type: evdevEvKey, Code: evdevBtnStart, Value: 0}, now.Add(1100*time.Millisecond), nil)
	if len(events) != 0 {
		t.Fatalf("Start release = %#v; F12 was already released", events)
	}

	bridge.setOn(true, now)
	bridge.feed(evdevEvent{Type: evdevEvKey, Code: evdevBtnSelect, Value: 1}, now.Add(time.Second), nil)
	events, _ = bridge.feed(evdevEvent{Type: evdevEvKey, Code: evdevBtnStart, Value: 1}, now.Add(1100*time.Millisecond), nil)
	if len(events) != 5 || events[0].Code != evdevKeyLeftAlt || events[1].Code != evdevKeyF4 ||
		events[2].Type != evdevEvSyn || events[3].Code != evdevKeyF4 || events[4].Code != evdevKeyLeftAlt {
		t.Fatalf("Select+Start = %#v; want Alt+F4", events)
	}
	for _, event := range events {
		if event.Code == evdevKeyF12 {
			t.Fatalf("Select+Start also sent keyboard toggle: %#v", events)
		}
	}
	bridge.tick(now.Add(3200*time.Millisecond), nil)
	if !bridge.quit {
		t.Fatal("Select+Start held for two seconds did not request quit")
	}
}

func TestWineStartRemainsEnterWithoutVirtualKeyboard(t *testing.T) {
	now := time.Unix(100, 0)
	bridge := mbridgeEngine{p: mbridgeParams{keys: true}}
	bridge.setOn(true, now)
	events, _ := bridge.feed(evdevEvent{Type: evdevEvKey, Code: evdevBtnStart, Value: 1}, now.Add(time.Second), nil)
	if len(events) != 1 || events[0].Code != evdevKeyEnter {
		t.Fatalf("direct Wine Start = %#v; want Enter", events)
	}
}
