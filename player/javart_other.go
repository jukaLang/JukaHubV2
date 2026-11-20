//go:build !windows

package main

// javart_other.go — where a Linux computer keeps its Javas: the
// distribution's /usr/lib/jvm (Debian, Ubuntu, Fedora, Arch), /usr/java and
// /opt. The handheld has none of these; its Java is JukaHub's.

// javaPlatformCandidates lists the java launchers in the usual folders,
// newest version first.
func javaPlatformCandidates() []string {
	var out []string
	for _, pattern := range []string{"/usr/lib/jvm/*", "/usr/java/*", "/opt/java/*", "/opt/jdk*"} {
		out = append(out, javaGlobJavas(pattern)...)
	}
	return out
}

// javaDesktopAvailable reports whether a windowed Java program has a desktop
// to open on: a PC with X11 or Wayland. The handheld has neither.
func javaDesktopAvailable() bool {
	if IsTrimuiDevice() {
		return false
	}
	return javaEnvSet("DISPLAY") || javaEnvSet("WAYLAND_DISPLAY")
}
