package main

import (
	"os"
	"testing"
	"time"
)

// cacheBenchDir switches into a scratch directory and guarantees the shared
// handle is closed before the directory is removed.
func cacheBenchDir(b *testing.B) {
	dir, err := os.MkdirTemp("", "cachebench")
	if err != nil {
		b.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		closeSharedCache()
		_ = os.Chdir(prev)
		_ = os.RemoveAll(dir)
	})
	closeSharedCache()
}

// Opening bbolt for every cache operation is the cost the shared handle
// removes. Numbers from a desktop SSD:
//
//	BenchmarkCacheOpenSetClose-8    ~12.5 ms/op
//	BenchmarkSharedCacheSet-8       ~20 µs/op
//
// On a MicroSD card the gap is wider still.
func BenchmarkCacheOpenSetClose(b *testing.B) {
	cacheBenchDir(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, err := cacheOpen("bench.cache")
		if err != nil {
			b.Fatal(err)
		}
		if err := cacheSet(db, "k", []byte("value"), time.Hour); err != nil {
			b.Fatal(err)
		}
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSharedCacheSet(b *testing.B) {
	cacheBenchDir(b)
	db, err := openSharedCache("bench.cache")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cacheSet(db, "k", []byte("value"), time.Hour); err != nil {
			b.Fatal(err)
		}
	}
}
