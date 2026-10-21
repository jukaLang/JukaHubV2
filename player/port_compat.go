package main

// port_compat.go — can this PortMaster port run on the handheld at all?
//
// PortMaster already knows which architectures a port ships, and it writes that
// into the port's own manifest (port.json, with an arch list). Reading that is
// far more reliable than a hand-curated list, so the manifest decides and the
// curated map below only overrides it where the community has checked a port by
// hand — for a build that is 32-bit only, or one that is known to work.
//
// The states are deliberately three, not five: the panel either shows a badge
// ("64-bit ready" / "32-bit only") or stays quiet when there is nothing to
// judge. A guess without a manifest is worse than no badge.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// portCompatState is the label the Ports panel shows.
type portCompatState int

const (
	portCompatUnknown   portCompatState = iota // no manifest to judge by
	portCompatSupported                        // ships an aarch64 (or arch-independent) build
	portCompat32BitOnly                        // armhf/i386 only: cannot run here
)

// portCompat is the verdict for one port folder.
type portCompat struct {
	State portCompatState
	Arch  []string // the architectures the manifest listed
	Note  string   // short human reason, e.g. "armhf only"
}

// curatedPortCompat pins the ports the community verified as broken or working
// on this chip, keyed by lower-cased folder name. A wrong "32-bit only" hides a
// working port, so an entry belongs here only after it was tried on a Smart Pro;
// everything not listed is judged from its own manifest, which is the normal
// case. This is a starter list, not a database.
var curatedPortCompat = map[string]portCompat{
	"sonic-mania": {State: portCompat32BitOnly, Note: "armhf build only — no ARM64 build"},
	"undertale":   {State: portCompat32BitOnly, Note: "x86 build only — no ARM build"},
	"gzdoom":      {State: portCompatSupported},
	"vkquake":     {State: portCompatSupported},
}

// archClass is what one architecture name means for this device, which is
// aarch64 with glibc 2.23.
type archClass int

const (
	archUnknown archClass = iota
	archARM64
	archARM32
	archIndependent
	archX86
)

// classifyArch maps the many spellings PortMaster and port authors use onto the
// four things that matter here.
func classifyArch(name string) archClass {
	a := strings.ToLower(strings.TrimSpace(name))
	switch a {
	case "":
		return archUnknown
	case "aarch64", "arm64", "arm64-v8a", "arm64v8", "arm64-gnu":
		return archARM64
	case "armhf", "armv7", "armv7l", "armv7a", "armv6", "armv6l", "arm":
		return archARM32
	case "all", "any", "noarch", "universal":
		return archIndependent
	case "i386", "i686", "x86", "x86_64", "amd64", "x64":
		return archX86
	}
	switch {
	case strings.HasPrefix(a, "aarch64"), strings.HasPrefix(a, "arm64"):
		return archARM64
	case strings.HasPrefix(a, "armv"), strings.HasPrefix(a, "armhf"):
		return archARM32
	case strings.HasPrefix(a, "x86"), strings.HasPrefix(a, "amd64"):
		return archX86
	}
	return archUnknown
}

// normalizeArchList trims, drops unclassifiable placeholder text, dedupes
// case-insensitively and sorts, so the panel and the tests see one stable list.
func normalizeArchList(arch []string) []string {
	seen := make(map[string]bool, len(arch))
	out := make([]string, 0, len(arch))
	for _, a := range arch {
		a = strings.TrimSpace(a)
		if a == "" || classifyArch(a) == archUnknown {
			continue
		}
		key := strings.ToLower(a)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil // callers treat nil as "no manifest", not as an empty claim
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// archFromJSONValue reads an arch list written as a JSON array or a single
// string. Anything else is ignored rather than guessed at.
func archFromJSONValue(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return splitArchList(t)
	}
	return nil
}

// splitArchList splits the comma/space separated form used by info.txt.
func splitArchList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '|'
	})
}

// portManifestArch reads arch information from a port directory. PortMaster
// stores {"arch":[…]}, but some ports nest it under "items" or "attr", and older
// ones only write an `arch=` line into info.txt, so all three are accepted.
// Only files directly inside dir are read.
func portManifestArch(dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}

	if raw, err := os.ReadFile(filepath.Join(dir, "port.json")); err == nil {
		var doc map[string]interface{}
		if err := json.Unmarshal(raw, &doc); err == nil {
			arches := archFromJSONValue(doc["arch"])
			if len(arches) == 0 {
				if items, ok := doc["items"].([]interface{}); ok {
					for _, item := range items {
						if m, ok := item.(map[string]interface{}); ok {
							arches = append(arches, archFromJSONValue(m["arch"])...)
						}
					}
				}
			}
			if len(arches) == 0 {
				if attr, ok := doc["attr"].(map[string]interface{}); ok {
					arches = append(arches, archFromJSONValue(attr["arch"])...)
				}
			}
			if len(arches) > 0 {
				return normalizeArchList(arches)
			}
		}
	}

	return normalizeArchList(archFromInfoTxt(filepath.Join(dir, "info.txt")))
}

// archFromInfoTxt reads `arch=aarch64,armhf` (or `Arch: …`) from an info.txt.
func archFromInfoTxt(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			key, value, ok = strings.Cut(line, ":")
		}
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "arch") {
			return splitArchList(value)
		}
	}
	return nil
}

// classifyPort turns an arch list into the panel's verdict. A port that lists
// any ARM64 or arch-independent build is supported: that is the build the
// device will install. My name for the second state is "32-bit only" because
// that is what the badge says; an x86-only build falls here too, since it also
// cannot run, and its Note carries the real reason.
func classifyPort(arch []string) portCompat {
	list := normalizeArchList(arch)
	if len(list) == 0 {
		return portCompat{State: portCompatUnknown}
	}

	var arm64, arm32, independent, x86 string
	for _, a := range list {
		switch classifyArch(a) {
		case archARM64:
			if arm64 == "" {
				arm64 = a
			}
		case archARM32:
			if arm32 == "" {
				arm32 = a
			}
		case archIndependent:
			if independent == "" {
				independent = a
			}
		case archX86:
			if x86 == "" {
				x86 = a
			}
		}
	}

	switch {
	case arm64 != "":
		return portCompat{State: portCompatSupported, Arch: list, Note: arm64 + " build"}
	case independent != "":
		return portCompat{State: portCompatSupported, Arch: list, Note: "architecture independent"}
	case arm32 != "":
		return portCompat{State: portCompat32BitOnly, Arch: list, Note: arm32 + " only"}
	default:
		return portCompat{State: portCompat32BitOnly, Arch: list, Note: x86 + " only — no ARM build"}
	}
}

// portCompatibility is the verdict for a port folder, with the curated list
// winning over the manifest (a manifest can claim a build the device still
// cannot run, and a hand check is the better source there).
// portCompatForLauncher resolves a port from its launcher script. PortMaster
// installs `<Name>.sh` beside a `<Name>/` data folder, and the manifest can sit
// in either place, so both are tried before the verdict is "unknown".
func portCompatForLauncher(launcher string) portCompat {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" {
		return portCompat{State: portCompatUnknown}
	}
	if c := portCompatibility(filepath.Dir(launcher)); c.State != portCompatUnknown {
		return c
	}
	stem := strings.TrimSuffix(filepath.Base(launcher), filepath.Ext(launcher))
	sibling := filepath.Join(filepath.Dir(launcher), stem)
	if c := portCompatibility(sibling); c.State != portCompatUnknown {
		return c
	}
	return portCompat{State: portCompatUnknown}
}

func portCompatibility(dir string) portCompat {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return portCompat{State: portCompatUnknown}
	}
	base := strings.ToLower(filepath.Base(dir))
	manifest := portManifestArch(dir)
	if pinned, ok := curatedPortCompat[base]; ok {
		if len(pinned.Arch) == 0 {
			pinned.Arch = manifest
		}
		return pinned
	}
	return classifyPort(manifest)
}

// portCompatBadge is the tiny label the Ports panel puts next to a row. Unknown
// ports get nothing: a badge that says "maybe" is noise.
func portCompatBadge(state portCompatState) string {
	switch state {
	case portCompatSupported:
		return "64-bit ready"
	case portCompat32BitOnly:
		return "32-bit only"
	}
	return ""
}
