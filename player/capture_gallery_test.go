package main

// capture_gallery_test.go — the screenshot gallery's game sections step with
// L1/R1 in the viewer (MED-5).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImageViewerStepsBetweenGameGroups(t *testing.T) {
	dir := t.TempDir()
	picture := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("picture"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	alpha1, alpha2, beta1 := picture("alpha-1.png"), picture("alpha-2.png"), picture("beta-1.png")

	oldGroups, oldFiles, oldIndex, oldPath := imageViewerGroups, imageViewerFiles, imageViewerIndex, imageViewerPath
	defer func() {
		imageViewerGroups, imageViewerFiles, imageViewerIndex, imageViewerPath = oldGroups, oldFiles, oldIndex, oldPath
	}()
	imageViewerGroups = []imgvGameGroup{
		{name: "Alpha", paths: []string{alpha1, alpha2}},
		{name: "Beta", paths: []string{beta1}},
	}
	imageViewerFiles = []string{alpha1, alpha2}
	imageViewerIndex = 0
	imageViewerPath = alpha1

	if !imgvGroupStep(1) {
		t.Fatal("L1/R1 did not step to the next game")
	}
	if imageViewerPath != beta1 || len(imageViewerFiles) != 1 || imageViewerIndex != 0 {
		t.Fatalf("next game showed %q with %d pictures at %d, want %q alone",
			imageViewerPath, len(imageViewerFiles), imageViewerIndex, beta1)
	}
	if !imgvGroupStep(1) || imageViewerPath != alpha1 {
		t.Fatalf("stepping past the last game did not wrap: got %q", imageViewerPath)
	}
	if !imgvGroupStep(-1) || imageViewerPath != beta1 {
		t.Fatalf("stepping back did not wrap to the last game: got %q", imageViewerPath)
	}

	// A folder without game sections keeps L1/R1 for zoom.
	imageViewerGroups = nil
	if imgvGroupStep(1) {
		t.Fatal("a single-section viewer swallowed L1/R1 zoom")
	}
}

func TestCapturePictureGroupNamesTheGame(t *testing.T) {
	cases := []struct {
		name, folder, want string
	}{
		{"game-Minecraft_2026-11-27_12-00-00.png", "", "Minecraft"},
		{"game-Half_Life_2026-11-27_12-00-00-2.png", "", "Half_Life"},
		{"Super Mario World - 2026-11-27_12-00-00.png", "", "Super Mario World"},
		{"Super Mario World_2026-11-27_12-00-00.png", "", "Super Mario World"},
		{"anything.png", "Secret of Mana", "Secret of Mana"},
		{"random.png", "", "Other screenshots"},
	}
	for _, tc := range cases {
		if got := capturePictureGroup(tc.name, tc.folder); got != tc.want {
			t.Errorf("capturePictureGroup(%q, %q) = %q, want %q", tc.name, tc.folder, got, tc.want)
		}
	}
}
