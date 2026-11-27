package main

import (
	"fmt"
	"testing"
)

func TestSceneScrollMemoryRestoresAndStaysBounded(t *testing.T) {
	sceneScrollForget()
	t.Cleanup(sceneScrollForget)

	// A first visit has no position to restore.
	if y, textY := sceneScrollRestore("Home"); y != 0 || textY != 0 {
		t.Fatalf("first visit restored (%d,%d), want (0,0)", y, textY)
	}

	sceneScrollRemember("Home", 120, 40)
	sceneScrollRemember("Browser", 300, 90)
	if y, textY := sceneScrollRestore("Home"); y != 120 || textY != 40 {
		t.Fatalf("Home restored (%d,%d), want (120,40)", y, textY)
	}
	// Remembering a scene again updates it instead of adding a second slot.
	sceneScrollRemember("Home", 240, 60)
	if y, textY := sceneScrollRestore("Home"); y != 240 || textY != 60 {
		t.Fatalf("Home restored (%d,%d) after update, want (240,60)", y, textY)
	}

	for i := 0; i < sceneScrollMax+5; i++ {
		sceneScrollRemember(fmt.Sprintf("scene-%d", i), int32(i), int32(i*2))
	}
	sceneScrolls.mu.Lock()
	got := len(sceneScrolls.states)
	sceneScrolls.mu.Unlock()
	if got != sceneScrollMax {
		t.Fatalf("remembered %d scenes, want the bound %d", got, sceneScrollMax)
	}
	if y, textY := sceneScrollRestore("Home"); y != 0 || textY != 0 {
		t.Fatalf("oldest scene survived eviction: (%d,%d)", y, textY)
	}
	last := fmt.Sprintf("scene-%d", sceneScrollMax+4)
	if y, textY := sceneScrollRestore(last); y != int32(sceneScrollMax+4) || textY != int32((sceneScrollMax+4)*2) {
		t.Fatalf("newest scene restored (%d,%d)", y, textY)
	}
}

func TestFocusPersistIsBounded(t *testing.T) {
	fe := NewFocusEngine()
	fe.graph = &FocusGraph{Nodes: []FocusNode{{Index: 7, Type: "button"}}}
	fe.current = 0
	// A fixed count, not the bound itself: a regression that removes the bound
	// must fail this test, not make it loop for billions of iterations.
	const attempted = 300
	if attempted <= focusPersistMax {
		t.Fatalf("attempted %d does not exceed the bound %d", attempted, focusPersistMax)
	}
	for i := 0; i < attempted; i++ {
		fe.Persist(fmt.Sprintf("scene-%d", i))
	}
	fe.mu.RLock()
	got := len(fe.persistFocus)
	_, oldest := fe.persistFocus["scene-0"]
	last := fmt.Sprintf("scene-%d", attempted-1)
	newest, newestOK := fe.persistFocus[last]
	fe.mu.RUnlock()
	if got != focusPersistMax {
		t.Fatalf("persisted %d scenes, want the bound %d", got, focusPersistMax)
	}
	if oldest {
		t.Fatal("oldest focus entry survived eviction")
	}
	if !newestOK || newest != 7 {
		t.Fatalf("newest focus entry = %d,%v, want 7,true", newest, newestOK)
	}
}
