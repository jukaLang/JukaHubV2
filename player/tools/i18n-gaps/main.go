// Command i18n-gaps reports which interface strings a language catalogue does
// not translate, and can write a starter file for a translator to fill in.
//
// There is no en.json: the source strings live in the Go UI code. The union
// of the shipped catalogues is used as the list of strings worth translating,
// so a key nobody has translated yet does not appear in the report. Run it
// from anywhere:
//
//	cd player && go run ./tools/i18n-gaps -dir i18n -out i18n/gaps
//
// With no -out it only prints the counts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type catalogue struct {
	Language string            `json:"language"`
	Name     string            `json:"name"`
	Strings  map[string]string `json:"strings"`
}

// analyze reads the catalogues in dir and returns each language's missing
// keys, measured against the union of every language's keys.
func analyze(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	all := map[string]bool{}
	catalogues := make(map[string]catalogue)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tour.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var cat catalogue
		if err := json.Unmarshal(data, &cat); err != nil || cat.Strings == nil {
			continue
		}
		if cat.Language == "" {
			cat.Language = strings.TrimSuffix(name, ".json")
		}
		catalogues[cat.Language] = cat
		for key := range cat.Strings {
			all[key] = true
		}
	}
	out := make(map[string][]string, len(catalogues))
	for code, cat := range catalogues {
		for key := range all {
			if value, ok := cat.Strings[key]; !ok || strings.TrimSpace(value) == "" {
				out[code] = append(out[code], key)
			}
		}
		sort.Strings(out[code])
	}
	return out, nil
}

// starter is a file a translator can fill in: every missing key with an
// empty value, in one place.
func starter(code string, keys []string) []byte {
	lines := []string{"{", `  "language": "` + code + `",`, `  "strings": {`}
	for i, key := range keys {
		encoded, _ := json.Marshal(key)
		comma := ","
		if i == len(keys)-1 {
			comma = ""
		}
		lines = append(lines, "    "+string(encoded)+": \"\""+comma)
	}
	lines = append(lines, "  }", "}", "")
	return []byte(strings.Join(lines, "\n"))
}

func main() {
	dir := flag.String("dir", "player/i18n", "folder with the language catalogues")
	out := flag.String("out", "", "write starter files to this folder")
	flag.Parse()
	missing, err := analyze(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	codes := make([]string, 0, len(missing))
	for code := range missing {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	total := 0
	for _, code := range codes {
		keys := missing[code]
		total += len(keys)
		fmt.Printf("%-6s %5d missing\n", code, len(keys))
		if *out != "" {
			if err := os.MkdirAll(*out, 0o755); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			path := filepath.Join(*out, code+".missing.json")
			if err := os.WriteFile(path, starter(code, keys), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	fmt.Printf("total  %5d missing entries\n", total)
}
