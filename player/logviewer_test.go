package main

import (
	"strings"
	"testing"
)

func TestTailLinesKeepsNewestEntries(t *testing.T) {
	lines := []string{"oldest", "old", "new", "newest"}

	got := tailLines(lines, 2)
	if len(got) != 2 || got[0] != "new" || got[1] != "newest" {
		t.Errorf("tailLines(4,2) = %v, want the two newest lines", got)
	}

	if got := tailLines(lines, 10); len(got) != 4 {
		t.Errorf("a panel taller than the log should show every line, got %v", got)
	}
	if got := tailLines(lines, 0); len(got) != 4 {
		t.Errorf("max<=0 should leave the lines untouched, got %v", got)
	}
	if got := tailLines(nil, 3); len(got) != 0 {
		t.Errorf("nil input = %v, want empty", got)
	}
}

// The rendered panel must show the newest line, not the first one captured at
// startup.
func TestLogPanelShowsNewestLine(t *testing.T) {
	t.Cleanup(func() { appLog.Clear() })
	appLog.Clear()
	for _, line := range []string{"boot", "middle", "latest"} {
		appLog.Write([]byte(line + "\n"))
	}

	lines := strings.Split(appLog.String(), "\n")
	visible := 1
	got := strings.Join(tailLines(lines, visible), "\n")
	if !strings.Contains(got, "latest") {
		t.Errorf("visible log tail = %q, want the newest line", got)
	}
}
