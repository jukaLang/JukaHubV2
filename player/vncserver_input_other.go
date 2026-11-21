//go:build !linux

package main

// vncserver_input_other.go — Windows and the rest: a game plays in a window
// of its own that JukaHub cannot type into, so a viewer's keys reach
// JukaHub's own screens only.

func vncPadSupported() bool                  { return false }
func vncPadSetCodes(mbridgeParams)           {}
func vncPadPress(action int, down bool) bool { return false }
func vncPadReleaseAll()                      {}
func vncPadClose()                           {}
