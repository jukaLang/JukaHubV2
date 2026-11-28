package main

// TEST-1 — the scene dump, not a test.
//
// The dump is a mode of the app (screenshotmode.go): `JukaHub --scene-dump
// <dir>` starts one child per canvas size, and each child walks every
// configured scene. These tests pin the plan the walk uses — every page, both
// sizes, unique file names, Exit left out — and the options that start it.
// The real render is checked on a machine: SDL_VIDEODRIVER=dummy ./JukaHub
// --scene-dump tmp/scenes.

import (
	"strings"
	"testing"
)

func TestSceneDumpOptions(t *testing.T) {
	opts := shotParseOptions([]string{"--scene-dump", "tmp/scenes"}, func(string) string { return "" })
	if !opts.on {
		t.Fatal("--scene-dump did not turn the mode on")
	}
	if opts.dumpDir != "tmp/scenes" {
		t.Fatalf("dumpDir = %q, want tmp/scenes", opts.dumpDir)
	}

	opts = shotParseOptions([]string{"--scene-dump=out", "--canvas", "960x720"}, func(string) string { return "" })
	if opts.dumpDir != "out" || opts.canvasW != 960 || opts.canvasH != 720 {
		t.Fatalf("dump options = %+v, want out at 960x720", opts)
	}

	// A canvas override alone is not a dump: it is how a dump child is told
	// which layout to use, and must not start a walk on its own.
	opts = shotParseOptions([]string{"--canvas", "1280x720"}, func(string) string { return "" })
	if opts.on || opts.dumpDir != "" || opts.canvasW != 1280 || opts.canvasH != 720 {
		t.Fatalf("canvas-only options = %+v", opts)
	}

	for _, bad := range []string{"", "960", "0x720", "200x200", "99999x720", "960x"} {
		if _, _, ok := shotCanvasSize(bad); ok {
			t.Errorf("shotCanvasSize(%q) accepted a bad canvas", bad)
		}
	}
}

func TestSceneDumpStepsCoverEveryScene(t *testing.T) {
	cfg := &Config{Scenes: []SceneConfig{
		{Name: "Main"}, {Name: "Tube"}, {Name: "Settings"},
		{Name: "Exit"}, {Name: "Main"},
	}}
	steps := shotDumpSteps(cfg, 1280, 720)
	want := []string{
		"1280x720/main.png",
		"1280x720/tube.png",
		"1280x720/settings.png",
		"1280x720/main-2.png",
	}
	if len(steps) != len(want) {
		t.Fatalf("dump has %d steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i, step := range steps {
		if step.file != want[i] {
			t.Errorf("step %d file = %q, want %q", i, step.file, want[i])
		}
		if step.settle != shotDumpSettle {
			t.Errorf("step %q settle = %v, want %v", step.file, step.settle, shotDumpSettle)
		}
		if step.enter == nil || step.ready == nil {
			t.Errorf("step %q has no enter/ready", step.file)
		}
	}
	for _, step := range steps {
		if strings.Contains(step.file, "exit") {
			t.Fatalf("Exit is a command, not a page, but the dump has %q", step.file)
		}
	}

	small := shotDumpSteps(cfg, 960, 720)
	if len(small) != len(want) {
		t.Fatalf("960 dump has %d steps, want %d", len(small), len(want))
	}
	for i, step := range small {
		if !strings.HasPrefix(step.file, "960x720/") {
			t.Errorf("960 step %d file = %q", i, step.file)
		}
	}

	if got := shotDumpSteps(nil, 1280, 720); got != nil {
		t.Fatalf("a nil config produced %d steps", len(got))
	}
}
