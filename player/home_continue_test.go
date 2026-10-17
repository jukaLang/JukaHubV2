package main

import "testing"

// The Continue card must start a video on the scene that owns the `video`
// element. Starting playback from Home used to run the decoder with nothing on
// screen (audio only), which looked like Continue doing nothing after the user
// backed out of a video.
func TestSceneWithVideoElementFindsPlayerScene(t *testing.T) {
	prev := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prev })

	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Layout: "home", Elements: []Element{{Type: "recent"}}},
		{Name: "Tube", Elements: []Element{{Type: "input"}, {Type: "video"}}},
		{Name: "IPStream", Elements: []Element{{Type: "video"}}},
	}}

	currentSceneIndex = 0
	if got := sceneWithVideoElement(config); got != 1 {
		t.Fatalf("from Home: sceneWithVideoElement = %d, want 1 (Tube)", got)
	}

	currentSceneIndex = 2
	if got := sceneWithVideoElement(config); got != 2 {
		t.Fatalf("on IPStream: sceneWithVideoElement = %d, want the current scene 2", got)
	}

	currentSceneIndex = 1
	if got := sceneWithVideoElement(config); got != 1 {
		t.Fatalf("on Tube: sceneWithVideoElement = %d, want 1", got)
	}
}

// Continue from Home has to leave the Home scene, otherwise the decoder runs
// with no `video` element to draw into and playback is invisible.
func TestEnsureVideoSceneSwitchesToPlayerScene(t *testing.T) {
	prevScene, prevPending, prevPhase := currentSceneIndex, pendingSceneIndex, transitionPhase
	t.Cleanup(func() {
		currentSceneIndex, pendingSceneIndex, transitionPhase = prevScene, prevPending, prevPhase
	})
	currentSceneIndex, pendingSceneIndex, transitionPhase = 0, -1, "none"

	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Layout: "home", Elements: []Element{{Type: "recent"}}},
		{Name: "Tube", Elements: []Element{{Type: "video"}}},
	}}

	if !ensureVideoScene(config) {
		t.Fatal("ensureVideoScene did not switch away from a scene with no video element")
	}
	if pendingSceneIndex != 1 {
		t.Fatalf("pendingSceneIndex = %d, want 1 (Tube)", pendingSceneIndex)
	}
	if transitionPhase == "none" {
		t.Fatal("ensureVideoScene did not start a scene transition")
	}
}

// Already being on a scene that can draw the player must not cause a scene
// change (playing a search result stays on the results grid).
func TestEnsureVideoSceneKeepsPlayerScene(t *testing.T) {
	prevScene, prevPending, prevPhase := currentSceneIndex, pendingSceneIndex, transitionPhase
	t.Cleanup(func() {
		currentSceneIndex, pendingSceneIndex, transitionPhase = prevScene, prevPending, prevPhase
	})
	currentSceneIndex, pendingSceneIndex, transitionPhase = 1, -1, "none"

	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Layout: "home"},
		{Name: "Tube", Elements: []Element{{Type: "video"}}},
	}}

	if ensureVideoScene(config) {
		t.Fatal("ensureVideoScene switched scenes while already on the player scene")
	}
	if transitionPhase != "none" {
		t.Fatalf("scene transition started unexpectedly (phase = %q)", transitionPhase)
	}
}

func TestSceneWithVideoElementWithoutPlayer(t *testing.T) {
	prev := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prev })
	currentSceneIndex = 0

	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Layout: "home", Elements: []Element{{Type: "recent"}}},
	}}
	if got := sceneWithVideoElement(config); got != -1 {
		t.Fatalf("sceneWithVideoElement = %d, want -1 when no scene has a video element", got)
	}
	if got := sceneWithVideoElement(nil); got != -1 {
		t.Fatalf("sceneWithVideoElement(nil) = %d, want -1", got)
	}
}
