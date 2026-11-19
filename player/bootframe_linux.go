//go:build linux

package main

// bootframe_linux.go — reading the page of /dev/fb0 that is on screen, for
// the boot frame (bootframe.go). The driver's own ioctls give the geometry and
// the pan (castcap_fbinfo_linux.go), so the page that is shown is the one read,
// whichever of the double buffer's halves it is.

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// bootFrameGrab returns the visible page, row by row at the buffer's stride,
// and its description line. Workers only: it reads a few megabytes.
func bootFrameGrab() ([]byte, string, error) {
	f, err := os.Open(castFBDevice)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	g, err := castFBIoctlGeom(f)
	if err != nil {
		return nil, "", err
	}
	// The launcher writes the picture back into every page from its first
	// column: a panned-sideways or wider-than-the-panel buffer would not line
	// up, so it is not kept.
	if !g.valid() || g.XOff != 0 || g.VW != g.W || g.VH < g.H {
		return nil, "", fmt.Errorf("framebuffer %dx%d (virtual %dx%d, offset %d) is not one the boot can show", g.W, g.H, g.VW, g.VH, g.XOff)
	}
	size := g.Stride * g.H
	if size <= 0 || size > bootFrameMaxBytes {
		return nil, "", errors.New("framebuffer page has an odd size")
	}
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, int64(g.YOff)*int64(g.Stride)); err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("framebuffer ended inside the visible page")
		}
		return nil, "", err
	}
	return buf, fmt.Sprintf("%d %d %d %d\n", g.W, g.H, g.BPP, g.Stride), nil
}
