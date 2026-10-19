package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestImageViewerStepWraps(t *testing.T) {
	prevFiles, prevIndex, prevPath := imageViewerFiles, imageViewerIndex, imageViewerPath
	prevZoom := imageViewerZoom
	t.Cleanup(func() {
		imageViewerFiles, imageViewerIndex, imageViewerPath = prevFiles, prevIndex, prevPath
		imageViewerZoom = prevZoom
	})

	imageViewerFiles = []string{"/photos/a.png", "/photos/b.png", "/photos/c.png"}
	imageViewerIndex = 0
	imageViewerPath = "/photos/a.png"

	imageViewerStep(-1)
	if imageViewerPath != "/photos/c.png" || imageViewerIndex != 2 {
		t.Fatalf("Left from the first image = %q (%d), want the last image", imageViewerPath, imageViewerIndex)
	}
	imageViewerStep(1)
	if imageViewerPath != "/photos/a.png" || imageViewerIndex != 0 {
		t.Fatalf("Right from the last image = %q (%d), want the first image", imageViewerPath, imageViewerIndex)
	}

	// A single image is a no-op rather than closing or reloading.
	imageViewerFiles = []string{"/photos/a.png"}
	imageViewerStep(1)
	if imageViewerPath != "/photos/a.png" {
		t.Fatalf("stepping a single-image list changed the path to %q", imageViewerPath)
	}
}

// A chat image preview must be clickable and open the viewer in remote mode,
// where the picture comes from the chat download cache instead of disk.
func TestChatImagePreviewOpensRemoteViewer(t *testing.T) {
	prevRects, prevPath, prevRemote := chatImageRects, imageViewerPath, imageViewerRemote
	t.Cleanup(func() {
		chatImageRects, imageViewerPath, imageViewerRemote = prevRects, prevPath, prevRemote
	})

	url := "https://example.com/cat.png"
	chatImageRects = []chatImageHit{{rect: sdl.Rect{X: 40, Y: 40, W: 120, H: 80}, url: url}}

	if handleChatImageClick(10, 10) {
		t.Fatal("a click outside the preview was consumed")
	}
	if !handleChatImageClick(60, 60) {
		t.Fatal("clicking the preview was not consumed")
	}
	if imageViewerPath != url {
		t.Fatalf("viewer path = %q, want the preview URL", imageViewerPath)
	}
	if !imageViewerRemote {
		t.Fatal("viewer did not switch to remote mode")
	}

	closeImageViewer()
	if imageViewerPath != "" || imageViewerRemote {
		t.Fatalf("closing left remote state behind: path=%q remote=%v", imageViewerPath, imageViewerRemote)
	}
}

func TestCloseImageViewerClearsList(t *testing.T) {
	prevFiles, prevIndex, prevPath := imageViewerFiles, imageViewerIndex, imageViewerPath
	t.Cleanup(func() {
		imageViewerFiles, imageViewerIndex, imageViewerPath = prevFiles, prevIndex, prevPath
	})
	imageViewerFiles = []string{"/a.png", "/b.png"}
	imageViewerIndex = 1
	imageViewerPath = "/b.png"

	closeImageViewer()
	if imageViewerPath != "" || len(imageViewerFiles) != 0 || imageViewerIndex != -1 {
		t.Fatalf("close left state behind: path=%q files=%v index=%d", imageViewerPath, imageViewerFiles, imageViewerIndex)
	}
}
