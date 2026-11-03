package main

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/veandco/go-sdl2/img"
)

// The window icon and the executable's resource icon (JukaHub.ico, generated
// from this same PNG by mkwinres.sh) share one source, so a missing or
// corrupted artwork file would quietly put the generic icon back on both.
func TestWindowIconArtworkIsUsable(t *testing.T) {
	if len(windowIconPNG) == 0 {
		t.Fatal("windowIconPNG is empty: JukaPlayer.png is missing from the embed")
	}
	src, err := png.Decode(bytes.NewReader(windowIconPNG))
	if err != nil {
		t.Fatalf("JukaPlayer.png does not decode as PNG: %v", err)
	}
	bounds := src.Bounds()
	if bounds.Dx() != bounds.Dy() {
		t.Fatalf("icon artwork must be square, got %dx%d", bounds.Dx(), bounds.Dy())
	}
	// Windows downscales from this across every frame in JukaHub.ico, the
	// largest of which is 256px, so the source must not be smaller.
	if bounds.Dx() < 256 {
		t.Fatalf("icon artwork is %dpx; 256px is needed for the jumbo frame", bounds.Dx())
	}
}

// applyWindowIcon feeds SDL the embedded bytes rather than a file next to the
// executable, so the decode itself has to work with no assets on disk.
func TestWindowIconSurfaceDecodesFromEmbed(t *testing.T) {
	newProbeFrame(t) // brings SDL video up with the dummy driver
	if err := img.Init(img.INIT_PNG); err != nil {
		t.Skipf("SDL_image PNG support unavailable: %v", err)
	}
	surface, err := windowIconSurface()
	if err != nil {
		t.Fatalf("could not decode the embedded icon into a surface: %v", err)
	}
	defer surface.Free()
	if surface.W != surface.H {
		t.Fatalf("icon surface is %dx%d; Windows expects a square window icon", surface.W, surface.H)
	}
	if surface.W < 256 {
		t.Fatalf("icon surface is %dpx wide, smaller than the 256px artwork", surface.W)
	}
}
