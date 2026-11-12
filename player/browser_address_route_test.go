package main

import (
	"strings"
	"testing"
)

func TestTypedWebAddressesOpenDirectly(t *testing.T) {
	for _, raw := range []string{"cnn.com", "https://cnn.com", "www.cnn.com/world"} {
		t.Run(raw, func(t *testing.T) {
			address, err := normalizeTextBrowserURL(raw)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(address, "duckduckgo") || !strings.HasPrefix(address, "https://") {
				t.Fatalf("address = %q, want a direct HTTPS URL", address)
			}
			rows := homeSearchResults(raw, homeSearchSources{online: true})
			if len(rows) != 1 || rows[0].Title != "Open "+raw || rows[0].Category != homeSearchGroupWeb {
				t.Fatalf("Home search rows = %+v, want one direct Open action", rows)
			}
		})
	}
}

func TestTypedSearchWordsStillSearchTheWeb(t *testing.T) {
	rows := homeSearchResults("cnn headlines", homeSearchSources{online: true})
	if len(rows) != 1 || !strings.HasPrefix(rows[0].Title, "Search the web for") {
		t.Fatalf("Home search rows = %+v, want a web search", rows)
	}
}
