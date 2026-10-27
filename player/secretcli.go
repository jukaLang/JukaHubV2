package main

// Command-line helpers for the encrypted values in jukaconfig.json.
//
//	JukaHub -set-secret discord_token          # value read from stdin
//	JukaHub -set-secret discord_token -value … # for scripts
//	JukaHub -check-secrets                     # verify, never print plaintext
//
// They run before SDL starts, so they also work over SSH on a handheld. The
// value is encrypted with the same key derivation the player uses at runtime,
// which means it can be prepared on a PC and copied to the device together with
// the config.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// secretCheck is the result of verifying one encrypted value.
type secretCheck struct {
	Name   string
	OK     bool
	Reason string
}

// handleSecretCommand runs a secret-management command if one was requested.
// It returns handled=false for a normal GUI launch.
func handleSecretCommand(args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}
	wantsSet, wantsCheck := false, false
	for _, a := range args {
		switch {
		case a == "-check-secrets" || a == "--check-secrets":
			wantsCheck = true
		case strings.HasPrefix(a, "-set-secret") || strings.HasPrefix(a, "--set-secret"):
			wantsSet = true
		}
	}
	if !wantsSet && !wantsCheck {
		return false, 0
	}

	fs := flag.NewFlagSet("secrets", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	setName := fs.String("set-secret", "", "secret name to store (value is read from stdin unless -value is given)")
	check := fs.Bool("check-secrets", false, "verify that every encrypted config value can be decrypted")
	configPath := fs.String("config", "jukaconfig.json", "config file to read or update")
	value := fs.String("value", "", "secret value (prefer stdin; -value is visible in the process list)")
	if err := fs.Parse(args); err != nil {
		return true, 2
	}

	if *check {
		checks, err := checkConfigSecrets(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return true, 1
		}
		if len(checks) == 0 {
			fmt.Printf("%s: no encrypted values\n", *configPath)
			return true, 0
		}
		failed := 0
		for _, c := range checks {
			if c.OK {
				fmt.Printf("  ok    %s\n", c.Name)
				continue
			}
			failed++
			fmt.Printf("  FAIL  %s: %s\n", c.Name, c.Reason)
		}
		if failed > 0 {
			return true, 1
		}
		return true, 0
	}

	name := strings.TrimSpace(*setName)
	if name == "" {
		fmt.Fprintln(os.Stderr, "error: -set-secret needs a name, e.g. -set-secret discord_token")
		return true, 2
	}
	secret := *value
	if secret == "" {
		var err error
		secret, err = readSecret(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: reading secret: %v\n", err)
			return true, 1
		}
	}
	if secret == "" {
		fmt.Fprintln(os.Stderr, "error: the secret is empty")
		return true, 2
	}
	if err := setConfigSecret(*configPath, name, secret); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return true, 1
	}
	fmt.Printf("stored %s in %s (encrypted, key %s)\n", name, *configPath, currentKeyInfo.ID)
	return true, 0
}

// readSecret reads one line from r without echoing it. The trailing newline is
// trimmed; inner characters are kept as typed.
func readSecret(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// secretTarget says where a named secret lives in the config document, and
// whether it should be encrypted (channel ids are not secrets).
type secretTarget struct {
	section string // "" means the top-level variables map
	key     string
	plain   bool
}

func secretLocation(name string) secretTarget {
	switch name {
	case "discord_token":
		return secretTarget{section: "channel_profile", key: "token"}
	case "discord_channel":
		return secretTarget{section: "channel_profile", key: "channel_id", plain: true}
	default:
		return secretTarget{section: "", key: name}
	}
}

// setConfigSecret encrypts value and writes it into path, preserving every
// other key in the document.
func setConfigSecret(path, name, value string) error {
	doc, perm, err := readConfigDocument(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	loc := secretLocation(name)
	stored := value
	if !loc.plain {
		stored, err = EncryptAPIToken(strings.TrimSpace(value), name)
		if err != nil {
			return err
		}
	}
	section := "variables"
	if loc.section != "" {
		section = loc.section
	}

	// Edit the field in place when possible: re-encoding the whole document
	// would reorder a 30-scene config and bury the one-line change in diff
	// noise. Fall back to a full rewrite for documents the scanner cannot read.
	if updated, ok := replaceJSONStringField(raw, section, loc.key, stored); ok {
		if err := writeConfigBytes(path, updated, perm); err == nil {
			return nil
		}
	}

	if loc.section == "" {
		vars, ok := doc["variables"].(map[string]interface{})
		if !ok {
			vars = map[string]interface{}{}
			doc["variables"] = vars
		}
		vars[loc.key] = stored
	} else {
		sectionObj, ok := doc[loc.section].(map[string]interface{})
		if !ok {
			sectionObj = map[string]interface{}{}
			doc[loc.section] = sectionObj
		}
		sectionObj[loc.key] = stored
	}
	return writeConfigDocument(path, doc, perm)
}

// replaceJSONStringField sets section.key to value by editing the raw JSON, so
// indentation, key order and everything outside that field stay byte-identical.
// It reports false when the section or key cannot be located safely.
func replaceJSONStringField(raw []byte, section, key, value string) ([]byte, bool) {
	start, end, ok := jsonObjectRange(raw, section)
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}

	fieldStart, fieldEnd, found := jsonStringFieldRange(raw[start:end], key)
	if found {
		// Grow with append instead of computing a capacity: the offset math is
		// attacker-influenced (via the config file), and an overflowed size
		// expression would panic or under-allocate.
		var out []byte
		out = append(out, raw[:start+fieldStart]...)
		out = append(out, encoded...)
		out = append(out, raw[start+fieldEnd:]...)
		return out, true
	}

	// Field missing: insert it as the last member of the section.
	insertAt := end
	trimmed := strings.TrimRight(string(raw[start:end]), " \t\r\n")
	prefix := ""
	if !strings.HasSuffix(strings.TrimSpace(trimmed), "{") {
		prefix = ", "
	}
	var out []byte
	out = append(out, raw[:insertAt]...)
	out = append(out, []byte(prefix+"\""+key+"\": "+string(encoded))...)
	out = append(out, raw[insertAt:]...)
	return out, true
}

// jsonObjectRange returns the byte offsets of the braces of the top-level
// object stored under key.
func jsonObjectRange(raw []byte, key string) (int, int, bool) {
	needle := []byte("\"" + key + "\"")
	i := bytes.Index(raw, needle)
	if i < 0 {
		return 0, 0, false
	}
	j := i + len(needle)
	for j < len(raw) && raw[j] != '{' {
		if raw[j] == '}' || raw[j] == ']' {
			return 0, 0, false
		}
		j++
	}
	if j >= len(raw) {
		return 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for k := j; k < len(raw); k++ {
		c := raw[k]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, k, true
			}
		}
	}
	return 0, 0, false
}

// jsonStringFieldRange locates a string value of key inside an object range,
// returning the span of the value (including both quotes).
func jsonStringFieldRange(obj []byte, key string) (int, int, bool) {
	needle := []byte("\"" + key + "\"")
	i := bytes.Index(obj, needle)
	if i < 0 {
		return 0, 0, false
	}
	j := i + len(needle)
	for j < len(obj) && obj[j] != ':' {
		j++
	}
	j++
	for j < len(obj) && (obj[j] == ' ' || obj[j] == '\t' || obj[j] == '\r' || obj[j] == '\n') {
		j++
	}
	if j >= len(obj) || obj[j] != '"' {
		return 0, 0, false
	}
	valueStart := j
	j++
	escaped := false
	for ; j < len(obj); j++ {
		if escaped {
			escaped = false
			continue
		}
		switch obj[j] {
		case '\\':
			escaped = true
		case '"':
			return valueStart, j + 1, true
		}
	}
	return 0, 0, false
}

// checkConfigSecrets verifies every ENC: value in the config.
func checkConfigSecrets(path string) ([]secretCheck, error) {
	doc, _, err := readConfigDocument(path)
	if err != nil {
		return nil, err
	}
	var checks []secretCheck

	verify := func(label, val string) {
		if !strings.HasPrefix(val, "ENC:") {
			return
		}
		_, derr := DecryptAPIToken(val, label)
		c := secretCheck{Name: label, OK: derr == nil}
		if derr != nil {
			c.Reason = derr.Error()
		}
		checks = append(checks, c)
	}

	if profile, ok := doc["channel_profile"].(map[string]interface{}); ok {
		if tok, ok := profile["token"].(string); ok {
			verify("channel_profile.token", tok)
		}
	}
	if vars, ok := doc["variables"].(map[string]interface{}); ok {
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if s, ok := vars[k].(string); ok {
				verify("variables."+k, s)
			}
		}
	}
	return checks, nil
}

func readConfigDocument(path string) (map[string]interface{}, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	return doc, perm, nil
}

func writeConfigDocument(path string, doc map[string]interface{}, perm os.FileMode) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeConfigBytes(path, data, perm)
}

// writeConfigBytes replaces path through the same durable path the player uses
// (temp file, fsync, rename, previous version as .bak). This command edits
// jukauser.json — the file holding the user's settings and secrets — so a card
// pulled mid-write must cost the last edit, not the document, and the .bak is
// the undo that a one-line CLI change otherwise lacks.
func writeConfigBytes(path string, data []byte, perm os.FileMode) error {
	return AtomicWrite(path, data, perm)
}
