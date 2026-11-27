package logx

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRingCapsAndMarks(t *testing.T) {
	r := NewRing(3, 0)
	for _, line := range []string{"a", "b", "c", "d"} {
		r.Append(line)
	}
	if got := r.String(); got != "b\nc\nd" {
		t.Fatalf("ring = %q, want the newest three lines", got)
	}
	if got := r.Mark(); got != 4 {
		t.Fatalf("Mark = %d, want every append", got)
	}
	if got := r.LinesSince(2, 0); strings.Join(got, "|") != "c|d" {
		t.Fatalf("LinesSince(2) = %v", got)
	}
	r.Clear()
	if r.String() != "" || r.Mark() != 4 {
		t.Fatalf("Clear lost the append mark: %q mark=%d", r.String(), r.Mark())
	}
	if got := r.LinesSince(3, 0); len(got) != 0 {
		t.Fatalf("a cleared ring still returned lines: %v", got)
	}
}

func TestRingByteCap(t *testing.T) {
	r := NewRing(10, 5)
	r.Append("123456789")
	if got := r.String(); got != "56789" {
		t.Fatalf("byte cap kept %q, want the newest five bytes", got)
	}
}

func TestHandlerRedactsAndWarns(t *testing.T) {
	r := NewRing(10, 0)
	var warned []string
	h := &Handler{
		Level:       slog.LevelDebug,
		StderrLevel: slog.Level(12), // keep the test's stderr quiet
		Ring:        r,
		Redact:      func(s string) string { return strings.ReplaceAll(s, "hunter2", "[redacted]") },
		WarnFile:    func(line string) { warned = append(warned, line) },
	}
	info := slog.NewRecord(time.Unix(0, 0), slog.LevelInfo, "login hunter2", 0)
	info.AddAttrs(slog.String("user", "juka"))
	if err := h.Handle(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	line := r.String()
	if strings.Contains(line, "hunter2") || !strings.Contains(line, "[redacted]") {
		t.Fatalf("ring line = %q", line)
	}
	if !strings.Contains(line, "user=juka") {
		t.Fatalf("attributes missing from %q", line)
	}
	if len(warned) != 0 {
		t.Fatalf("an info line reached the warning file: %v", warned)
	}
	warn := slog.NewRecord(time.Unix(0, 0), slog.LevelWarn, "careful", 0)
	if err := h.Handle(context.Background(), warn); err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "WARN  careful") {
		t.Fatalf("warning file = %v", warned)
	}
}

func TestHandlerForcedDebug(t *testing.T) {
	forced := false
	h := &Handler{Level: slog.LevelInfo, StderrLevel: slog.Level(12), Ring: NewRing(10, 0), ForceDebug: func() bool { return forced }}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug enabled before the switch")
	}
	forced = true
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("the runtime debug switch was ignored")
	}
	child := h.WithGroup("net").WithAttrs([]slog.Attr{slog.String("host", "a b")})
	rec := slog.NewRecord(time.Unix(0, 0), slog.LevelInfo, "x", 0)
	if err := child.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := h.Ring.String(); !strings.Contains(got, "net.host=\"a b\"") {
		t.Fatalf("grouped attribute = %q", got)
	}
}
