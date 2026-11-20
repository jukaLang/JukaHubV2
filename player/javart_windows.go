//go:build windows

package main

// javart_windows.go — where Windows keeps its Javas: the JavaSoft registry
// keys Oracle's and most other installers write (JavaHome per version), and
// the Program Files folders of the common builds (Adoptium, Oracle,
// Microsoft, Azul Zulu, BellSoft Liberica, Amazon Corretto).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// javaPlatformCandidates lists java.exe from the registry, then from the
// Program Files folders, newest version first within each.
func javaPlatformCandidates() []string {
	var out []string
	// The 64-bit view first, then the 32-bit one: a 32-bit Java runs
	// programs too. Named views read the same keys whatever JukaHub is built
	// for.
	for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		for _, kind := range []string{"JDK", "JRE", "Java Runtime Environment", "Java Development Kit"} {
			out = append(out, javaRegistryJavas(view, `SOFTWARE\JavaSoft\`+kind)...)
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		base := strings.TrimSpace(os.Getenv(env))
		if base == "" {
			continue
		}
		for _, vendor := range []string{"Eclipse Adoptium", "Java", "Microsoft", "Zulu", "BellSoft", "Amazon Corretto"} {
			out = append(out, javaGlobJavas(filepath.Join(base, vendor, "*"))...)
		}
	}
	return out
}

// javaRegistryJavas reads JavaHome of every version under one JavaSoft key
// in one registry view, newest version first (the key lists "1.8" before
// "21").
func javaRegistryJavas(view uint32, path string) []string {
	parent, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.ENUMERATE_SUB_KEYS|view)
	if err != nil {
		return nil
	}
	subs, _ := parent.ReadSubKeyNames(-1)
	_ = parent.Close()
	sort.Slice(subs, func(i, j int) bool { return javaNameNewer(subs[i], subs[j]) })
	var homes []string
	for _, sub := range subs {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path+`\`+sub, registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		home, typ, err := k.GetStringValue("JavaHome")
		_ = k.Close()
		if err != nil {
			continue
		}
		if typ == registry.EXPAND_SZ {
			if expanded, xerr := registry.ExpandString(home); xerr == nil {
				home = expanded
			}
		}
		home = strings.Trim(strings.TrimSpace(home), `"`)
		if home == "" || !filepath.IsAbs(home) {
			continue
		}
		homes = append(homes, filepath.Clean(home))
	}
	var out []string
	for _, h := range homes {
		if p := filepath.Join(h, "bin", "java.exe"); regularFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// javaDesktopAvailable is always true on Windows: every program has the
// desktop.
func javaDesktopAvailable() bool { return true }
