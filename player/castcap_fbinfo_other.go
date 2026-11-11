//go:build !linux

package main

// castcap_fbinfo_other.go — no framebuffer driver to ask outside Linux. Screen
// Share never starts a game reader there (castGamesSupported), so these only
// keep the package building.

import (
	"errors"
	"os"
)

var errCastNoFBIoctl = errors.New("framebuffer ioctls need the handheld's Linux")

func castFBIoctlGeom(f *os.File) (castFBGeom, error) { return castFBGeom{}, errCastNoFBIoctl }

func castFBIoctlPan(f *os.File) (int, int, error) { return 0, 0, errCastNoFBIoctl }
