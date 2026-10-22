# Machine stats (`machine_stats`)

Daemon half of leoterm's "Machines" feature. The app spec is leoterm `docs/superpowers/specs/2026-10-21-leo-machines.md` (the "Stats" section). Evan approved it. leoterm agreed to the amendments below on 2026-10-21.

## Routes
- Socket: `GET /machine`.
- Web: `GET /api/v1/machine`, with the same auth as `/api/v1/state`.
- Both routes are advertised through the `machine_stats` feature flag, wherever the daemon already lists its features.

## Response
```json
{
  "sampled_at": "RFC3339",
  "cpu":    {"percent": 0-100 | null, "cores": int},
  "memory": {"used_bytes": int, "total_bytes": int} | null,
  "disk":   {"path": string, "used_bytes": int, "total_bytes": int} | null
}
```

## Semantics
- **cpu.percent** is whole-machine usage since the previous sample. It is `null` on the first sample, and also `null` when the previous sample is more than 60s old; in that case the baseline is reset.
- **cpu.cores** is the logical CPU count.
- **memory.used_bytes**:
  - macOS: app + wired + compressed memory, where app = internal − purgeable (Activity Monitor's definition).
  - Linux: MemTotal − MemAvailable. If MemAvailable is missing, it falls back to MemFree + Buffers + Cached.
- **disk** is statfs on the daemon's state dir:
  - total = f_blocks × bsize;
  - used = total − f_bavail × bsize;
  - path = the state dir itself.
- **Per-section failure:** a section whose probe fails is `null`, and the route still returns 200. Errors are logged and never put in the response.
- **Caching:** the daemon samples on request and caches the result for 1s. Concurrent callers share one sample. There is no background sampler.
- **Load:** the app polls every 5s per machine, so the route must stay cheap. No subprocesses (`vm_stat`, `top`); use syscalls or sysctls only.

## Constraints
- Release builds use `CGO_ENABLED=0`, so the macOS probes must be cgo-free. Verify them on a CGO_ENABLED=0 build, not only under `go test`.
- Probes sit behind an injected interface so that the cache, baseline and null logic can be unit-tested with fakes. Inject a clock.
- Other platforms (anything that is not darwin or linux): all three sections are `null` except cpu.cores, and the route still returns 200.

## Ships in
v0.57.0. Ping leoterm when it is tagged.
