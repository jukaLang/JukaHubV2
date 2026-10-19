# Library evaluation — Trimui Smart Pro

Target: ARM64 Linux, 1 GB RAM, MicroSD storage, SDL2 (cgo) renderer, 40+ config
driven scenes. A dependency is only worth taking if it produces a **measured**
gain at that constraint level, not just cleaner code.

## Verdicts

| Library | Verdict | Why |
|---------|---------|-----|
| `ebiten/v2` | ❌ No | Replaces the entire renderer and input loop. The UI is already GPU-backed through SDL2; the win would be code aesthetics, not frame time. Adds GL/EGL build and runtime dependencies to a handheld that is currently patched as a single binary + static SDL libs. |
| `aarzilli/nucular` | ❌ No | An immediate-mode GUI would mean rewriting every scene and the focus graph. No measured rendering gain over the existing cached-text SDL2 path. |
| `etcd-io/bbolt` | ✅ Already used | `cache.go` and the thumbnail store already use bbolt. Improved below instead of adding anything. |
| `dgraph-io/badger` | ❌ No | LSM write amplification and memory use are the wrong trade for a 1 GB device. The cache is a few hundred small thumbnails, not a large media metadata corpus. |
| `sourcegraph/conc` | ❌ No | The only real benefit here is panic-safe task groups; that is ~10 lines with `recover` + `sync.WaitGroup`. A new dependency is not worth it for this codebase. |
| `panjf2000/ants/v2` | ❌ No | Prefetch is I/O-bound, not CPU-bound. Reusing goroutines does not make Wi-Fi faster, and a work queue adds head-of-line blocking. A plain 2–3 worker channel pool was enough (measured below). |
| `google/gopacket` | ❌ No | Raw sockets need root/pcap privileges that the handheld does not grant, and it solves discovery tooling, not performance. |
| `spf13/afero` | ❌ No (for now) | Useful for testability, but migrating every `os.*` call is broad churn with zero runtime gain. Revisit only if remote save sync becomes a feature. |

## What was implemented instead

`thumbstore.go` had one prefetch goroutine and opened/closed the bbolt cache
database for **every** thumbnail. Both costs showed up in measurements, so the
fix stayed dependency-free:

1. **Bounded prefetch pool** — 2 workers on handhelds, 3 on desktop, with an
   in-flight `sync.Map` so a grid re-enqueueing a visible row does not download
   the same image twice.
2. **Shared bbolt handle** — `openSharedCache` keeps one handle per cache path
   instead of open/mmap/close per image; `closeSharedCache` exists for tests
   that change working directory.
3. **Deterministic concurrency test** — `TestThumbnailPrefetchRunsConcurrently`
   only passes if two requests are genuinely in flight at once.
4. **Benchmarks** — `BenchmarkCacheOpenSetClose`, `BenchmarkSharedCacheSet`,
   `BenchmarkThumbnailPrefetch` (`workers=1,2,3`).

## Measurements

Environment: Windows 11, i7-1165G7, local `httptest` server with 10 ms simulated
latency, 8 unique thumbnails per batch. Commands:

```
go test -run '^$' -bench 'BenchmarkCache' -benchtime 10x .
go test -run '^$' -bench 'BenchmarkThumbnailPrefetch' -benchtime 10x .
```

| Scenario | Time per 8-image batch |
|----------|------------------------|
| 1 worker, bbolt opened per image | ~200–260 ms |
| 1 worker, shared bbolt handle | 105 ms |
| 2 workers, shared handle (handheld setting) | 56 ms |
| 3 workers, shared handle (desktop setting) | 42 ms |

| Cache operation | Before | After |
|-----------------|--------|-------|
| bbolt open + set + close | 9.8 ms | — |
| set through shared handle | — | 3.6 ms (write transaction/fsync still dominates) |

So the end-to-end prefetch path is roughly **4x faster** on this machine, with
2 workers giving most of the gain and 3 adding ~25% more. These numbers are from
a desktop SSD; on a MicroSD card the per-open cost is higher, so the relative
gain should be larger, but that needs an on-device run to confirm.

## Side fix found while measuring

Running the suite repeatedly exposed a crash (`Exception 0xc0000005` in
`SDL_RenderReadPixels`) that reproduced within a few runs of
`TestEmbeddedPlayerDecodesFrames` followed by the fullscreen overlay tests. The
decoder goroutine outlived `StopEmbeddedPlayback`, so a later test destroyed and
recreated the software renderer while ffmpeg was still being read. The playback
goroutines are now tracked with `videoDecoderWG`; the decode test waits for them
(`waitForVideoDecoders`) and drops the last texture before the renderer that owns
it is destroyed. Eight consecutive full-suite runs passed after the change.

### Not changed

- Keeping the bbolt handle open for the dictionary/packages caches too is the
  obvious follow-up (same per-open cost), but those paths are user-triggered and
  infrequent, so they were left alone.
- `NoSync` on the cache database was rejected: one power loss can corrupt the
  whole bbolt file, and the fsync cost is only ~3.6 ms per downloaded image.
