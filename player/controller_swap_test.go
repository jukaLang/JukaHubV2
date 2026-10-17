package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestAbButtonsSwappedReadsSetting(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if abButtonsSwapped(config) {
		t.Fatal("default must not swap A/B")
	}
	config.Variables.Custom["swapABButtons"] = "true"
	if !abButtonsSwapped(config) {
		t.Fatal("swapABButtons=true must enable the swap")
	}
	config.Variables.Custom["swapABButtons"] = false
	if abButtonsSwapped(config) {
		t.Fatal("swapABButtons=false must disable the swap")
	}
	if abButtonsSwapped(nil) {
		t.Fatal("nil config must not swap")
	}
}

func TestApplyButtonSwapFlipsABOnly(t *testing.T) {
	on := &Config{Variables: Variables{Custom: map[string]interface{}{"swapABButtons": true}}}

	a := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_A}
	applyButtonSwap(on, a)
	if a.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("A should flip to B, got %v", a.Button)
	}

	b := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_B}
	applyButtonSwap(on, b)
	if b.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("B should flip to A, got %v", b.Button)
	}

	y := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_Y}
	applyButtonSwap(on, y)
	if y.Button != sdl.CONTROLLER_BUTTON_Y {
		t.Fatalf("Y must be left alone, got %v", y.Button)
	}

	off := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	x := &sdl.ControllerButtonEvent{Button: sdl.CONTROLLER_BUTTON_A}
	applyButtonSwap(off, x)
	if x.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("disabled setting must not flip, got %v", x.Button)
	}
}
