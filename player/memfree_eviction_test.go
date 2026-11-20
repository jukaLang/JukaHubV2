package main

import (
	"testing"
	"time"
)

func TestMemFreeFullClearSupersedesRecentTrim(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Second)
	old := now.Add(-memFreeEvictEvery)
	for _, tc := range []struct {
		name                 string
		last                 time.Time
		lastLevel, requested int32
		want                 bool
	}{
		{"first clear", time.Time{}, 0, memEvictTrim, true},
		{"repeat trim", recent, memEvictTrim, memEvictTrim, false},
		{"AI after trim", recent, memEvictTrim, memEvictFull, true},
		{"repeat full", recent, memEvictFull, memEvictFull, false},
		{"trim after full", recent, memEvictFull, memEvictTrim, false},
		{"expired full", old, memEvictFull, memEvictFull, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := memFreeEvictDue(now, tc.last, tc.lastLevel, tc.requested); got != tc.want {
				t.Fatalf("evict due = %v, want %v", got, tc.want)
			}
		})
	}
}
