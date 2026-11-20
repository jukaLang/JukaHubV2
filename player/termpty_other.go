//go:build !linux && !windows

package main

// termpty_other.go — systems JukaHub does not ship for (macOS, the BSDs):
// no session, and the Terminal says interactive programs need the handheld.

func termPtySpawn(c termPtyCommand, env []string, dir string, cols, rows int) (termPtyProc, error) {
	return nil, errTermPtyUnsupported
}
