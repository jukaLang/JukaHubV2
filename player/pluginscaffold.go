package main

// pluginscaffold.go — `JukaHub --new-plugin <name>` and the files it writes.
//
// A plugin is a folder under plugins/ with a plugin.json and an entry script.
// The scaffold gives an author the smallest manifest that loads (one action, a
// least-privilege empty permission list) and a POSIX sh entry that runs on the
// handheld, where Python is not shipped.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	// plugSchemaVersion is the manifest schema this JukaHub reads. A manifest
	// without the field is the first schema, so old plugins keep loading.
	plugSchemaVersion = 1
	// pluginScaffoldDefaultVersion is the version a new plugin starts at.
	pluginScaffoldDefaultVersion = "0.1.0"
)

var pluginNamePattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
})

// plugSchemaProblem says why a manifest's schemaVersion cannot be read here,
// or "" when it can. A missing field is schema 1. Pure.
func plugSchemaProblem(v int) string {
	if v < 0 {
		return "plugin.json has a negative schemaVersion"
	}
	if v > plugSchemaVersion {
		return fmt.Sprintf("plugin.json uses schemaVersion %d; this JukaHub reads up to %d", v, plugSchemaVersion)
	}
	return ""
}

// pluginScaffoldManifest is the starter manifest, field for field what the
// loader reads. Kept as a struct so the file is stable and easy to diff.
type pluginScaffoldManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Name          string       `json:"name"`
	Title         string       `json:"title"`
	Version       string       `json:"version"`
	Description   string       `json:"description"`
	Author        string       `json:"author"`
	Entry         string       `json:"entry"`
	Icon          string       `json:"icon"`
	Enabled       bool         `json:"enabled"`
	Permissions   []string     `json:"permissions"`
	Actions       []plugAction `json:"actions"`
	OnEnable      string       `json:"onEnable"`
	Quick         []string     `json:"quick"`
}

// pluginScaffoldTitle turns a plugin id into the title the Plugins page shows.
func pluginScaffoldTitle(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	if len(parts) == 0 {
		return name
	}
	return strings.Join(parts, " ")
}

// newPluginScaffold creates root/name with a loadable manifest, a POSIX sh
// entry that answers the manifest's action, and a README. It refuses an
// unsafe name and never writes into an existing folder.
func newPluginScaffold(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if !pluginNamePattern().MatchString(name) {
		return "", errors.New("use a lower-case name of letters, digits, '-' or '_' (at most 32 characters)")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(root, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%s already exists", target)
		}
		return "", err
	}
	cleanup := func(err error) (string, error) {
		_ = os.RemoveAll(target)
		return "", err
	}
	title := pluginScaffoldTitle(name)
	manifest := pluginScaffoldManifest{
		SchemaVersion: plugSchemaVersion,
		Name:          name,
		Title:         title,
		Version:       pluginScaffoldDefaultVersion,
		Description:   title + " for JukaHub.",
		Author:        "",
		Entry:         "main.sh",
		Icon:          "apps",
		Enabled:       true,
		Permissions:   []string{},
		Actions:       []plugAction{{ID: "hello", Label: "Say hello"}},
		OnEnable:      "hello",
		Quick:         []string{"hello"},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return cleanup(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(target, "plugin.json"), data, 0o644); err != nil {
		return cleanup(err)
	}
	script := "#!/bin/sh\n" +
		"# " + title + ": the action id is $1; custom variables follow as --name=value.\n" +
		"case \"$1\" in\n" +
		"    hello) echo \"Hello from " + name + "\" ;;\n" +
		"    *) echo \"Unknown action: $1\" >&2; exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(target, "main.sh"), []byte(script), 0o755); err != nil {
		return cleanup(err)
	}
	readme := "# " + title + "\n\n" +
		"A JukaHub plugin scaffolded with `JukaHub --new-plugin " + name + "`.\n\n" +
		"- `plugin.json` is the versioned manifest (`schemaVersion` " + fmt.Sprint(plugSchemaVersion) + ").\n" +
		"- `main.sh` answers the `hello` action; add action ids to the manifest and to its `case`.\n" +
		"- It asks for no permissions. Add only what it needs: `network`, `files`, `programs`, `settings` (see `docs/PLUGINS.md`).\n" +
		"- Switch it on in Settings → Plugins; its button then appears in the Start menu's Plugins group.\n"
	if err := os.WriteFile(filepath.Join(target, "README.md"), []byte(readme), 0o644); err != nil {
		return cleanup(err)
	}
	return target, nil
}

// handleNewPluginCommand answers `JukaHub --new-plugin <name> [--plugins-dir
// <folder>]` before SDL starts, so it works from a terminal without a display.
func handleNewPluginCommand(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	name := ""
	switch {
	case args[0] == "--new-plugin":
		if len(args) > 1 {
			name = args[1]
			args = args[2:]
		} else {
			args = nil
		}
	case strings.HasPrefix(args[0], "--new-plugin="):
		name = strings.TrimPrefix(args[0], "--new-plugin=")
		args = args[1:]
	default:
		return false, 0
	}
	root := pluginDir
	if root == "" {
		root = "plugins"
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--plugins-dir" {
			root = args[i+1]
		}
	}
	if strings.TrimSpace(name) == "" {
		fmt.Fprintln(os.Stderr, "usage: JukaHub --new-plugin <name> [--plugins-dir <folder>]")
		return true, 2
	}
	path, err := newPluginScaffold(root, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "new plugin: "+err.Error())
		return true, 1
	}
	fmt.Println("Created " + path)
	return true, 0
}
