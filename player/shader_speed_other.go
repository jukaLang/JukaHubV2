//go:build !linux

package main

// shader_speed_other.go — no framebuffer to count flips on: the shader speed
// check reads RetroArch's own log after the game (shader_speed.go).

import "errors"

// shaderFlipCounter is the Linux flip counter's stand-in.
type shaderFlipCounter struct{}

func shaderSpeedFramesPossible() bool { return false }

func shaderSpeedFlipsOpen() (*shaderFlipCounter, error) {
	return nil, errors.New("no framebuffer on this system")
}

func (c *shaderFlipCounter) count() uint64 { return 0 }

func (c *shaderFlipCounter) alive() bool { return false }

func (c *shaderFlipCounter) setFast(on bool) {}

func (c *shaderFlipCounter) close() {}
