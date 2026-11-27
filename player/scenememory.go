package main

import "sync"

// scenememory.go — the scroll position a scene had when it was left, so Back
// returns to the row the player was looking at. Focus already remembers its
// own element (focus.go); this is the scroll beside it.
//
// The map is bounded: a long session can visit more transient scenes than a
// small card wants to remember, and the oldest entry is evicted first. Only
// the main thread writes the two globals this stores; the mutex keeps a test
// or a future background reader honest.

const sceneScrollMax = 64

type sceneScrollState struct {
	y     int32
	textY int32
}

var sceneScrolls = struct {
	mu     sync.Mutex
	states map[string]sceneScrollState
	order  []string
}{}

// sceneScrollRemember records a scene's scroll before it is left.
func sceneScrollRemember(sceneName string, y, textY int32) {
	if sceneName == "" {
		return
	}
	sceneScrolls.mu.Lock()
	defer sceneScrolls.mu.Unlock()
	if sceneScrolls.states == nil {
		sceneScrolls.states = make(map[string]sceneScrollState)
	}
	if _, seen := sceneScrolls.states[sceneName]; !seen {
		sceneScrolls.order = append(sceneScrolls.order, sceneName)
		if len(sceneScrolls.order) > sceneScrollMax {
			oldest := sceneScrolls.order[0]
			sceneScrolls.order = sceneScrolls.order[1:]
			delete(sceneScrolls.states, oldest)
		}
	}
	sceneScrolls.states[sceneName] = sceneScrollState{y: y, textY: textY}
}

// sceneScrollRestore returns the scroll a scene had, or zero on its first
// visit.
func sceneScrollRestore(sceneName string) (y, textY int32) {
	sceneScrolls.mu.Lock()
	defer sceneScrolls.mu.Unlock()
	if state, ok := sceneScrolls.states[sceneName]; ok {
		return state.y, state.textY
	}
	return 0, 0
}

// sceneScrollForget drops every remembered position (tests and a full reset).
func sceneScrollForget() {
	sceneScrolls.mu.Lock()
	sceneScrolls.states = nil
	sceneScrolls.order = nil
	sceneScrolls.mu.Unlock()
}
