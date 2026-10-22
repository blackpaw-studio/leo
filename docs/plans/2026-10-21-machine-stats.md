# Plan: machine stats (`machine_stats`)

Planner output (d-5a07f83ebb3a), checked against docs/specs/2026-10-21-machine-stats.md. Spec wins on conflict.

**macOS probe recommendation: use `github.com/ebitengine/purego` directly, imported only from `_darwin.go` files. Skip gopsutil.**
- gopsutil v4's darwin memory doesn't match the spec. `mem/mem_darwin.go:95-121` calls `host_statistics(HOST_VM_INFO)`, the 32-bit struct, which has no internal, purgeable or compressor counts. It computes `Used = total − (free + inactive)`.
- gopsutil is cgo-free only because it uses purego anyway: `internal/common/common_darwin.go:13,49` (`purego.Dlopen("/usr/lib/libSystem.B.dylib")`).
- gopsutil master's `go.mod` requires go 1.26.0. This repo is on 1.25.1. purego main declares go 1.25.0, and its README lists macOS amd64/arm64 as Tier 1 with `CGO_ENABLED=0` support.
- `x/sys/unix` sysctls can't cover it alone. macOS has no CPU-ticks sysctl, so the CPU read needs `host_statistics(HOST_CPU_LOAD_INFO)`, a Mach call.
- The `vm_statistics64` layout comes from xnu `osfmk/mach/vm_statistics.h`. Rev1 is 152 bytes, so the count is 38; `internal_page_count` is at offset 140.

**Spec ambiguities.** The plan assumes the first option in each case:
1. **Envelope.** Assumed: the spec object is the `data` of the usual `{"ok":true,"data":…}`, like every other route. leoterm should confirm.
2. **Auth parity.** Assumed: same as `/api/v1/state`, so the operator, agent and device tokens and trusted-proxy SSO all get 200, and API-client tokens get 403 (`auth_client.go:136`). The alternative would be operator-only.
3. **Linux CPU.** Assumed: idle = idle + iowait, with guest and guest_nice excluded from the total. The spec doesn't say.
4. **Darwin tick wrap.** Darwin ticks are 32-bit counters and wrap after roughly a month of uptime on a many-core idle Mac. Assumed: any counter going down counts as a reset, giving `null` and a new baseline.
5. **Failed CPU read.** Assumed: it keeps the previous baseline, since the 60s rule already limits its age. A boundary of exactly 60s still produces a value.
6. **Linux cores.** Assumed: count the `cpuN` lines in `/proc/stat` (all CPUs), not `runtime.NumCPU` (which follows CPU affinity).
7. **Feature flag.** It is listed only in the events hello, via `observe.Features()` on both `/events` routes. `/health` has no features field, and the plan doesn't add one.
8. **Other platforms.** Assumed: disk is `null` there even though statfs works on the BSDs, following the spec literally.



## Plan

Spec: `docs/specs/2026-10-21-machine-stats.md`. Commit after each step. Tests use `-race` and a fake clock, with no sleeps. purego is imported **only** in `*_darwin.go` files. On Linux with `CGO_ENABLED=0`, purego would switch on its fake cgo runtime.

## Design
- **New package `internal/machine`** (no internal imports).
  - `Stats{SampledAt time.Time; CPU CPU; Memory *Memory; Disk *Disk}`
  - `CPU{Percent *float64; Cores int}`
  - `Memory{UsedBytes, TotalBytes uint64}`
  - `Disk{Path string; UsedBytes, TotalBytes uint64}`
  - JSON tags follow the spec. There is no `omitempty`, so failed sections serialize as `null`.
- **`Probe` interface:** `Cores() int` (never fails), `CPUTicks() (CPUTicks{Busy, Idle uint64}, error)`, `Memory() (Memory, error)`, `Disk() (Disk, error)`. `NewProbe(stateDir string) Probe` is defined once per platform file.
- **`Sampler`:** built with `NewSampler(p Probe, opts...)`, with `WithClock(func() time.Time)` and `WithLogf(...)` (default `log.Printf`). `Sample() Stats`.
  - **Cache.** It holds a mutex, the cached `Stats` with its time, and an in-flight call (`done chan`). If the cache is younger than 1s it returns the cache. If a call is in flight it waits on that call; an unexported `onWait` hook lets tests detect this. Otherwise it runs the probes outside the lock, then locks to compute the CPU delta, update the baseline and store the cache.
  - **CPU percent.** With no baseline, a baseline older than 60s, any counter going down, or a total delta of 0, the result is `null` and the current reading becomes the new baseline. Otherwise percent = 100 × Δbusy / Δtotal, clamped to 0–100. A CPU read error gives `null` and keeps the old baseline.
  - **Logging.** One line per section when its error text changes, plus one "recovered" line. A probe that keeps failing doesn't flood the log at 1 sample/s.
- **Probe files:**
  - `procfs.go` (no build tag, so it runs on macOS too): `parseProcStat(io.Reader) (CPUTicks, cores int, err)` and `parseMeminfo(io.Reader) (Memory, error)`. `calc.go`: `diskFromStatfs(path, blocks, bavail, bsize)` and `darwinUsed(internal, purgeable, wire, compressor uint32, pageSize uint64)`. Both clamp so the result never underflows.
  - `probe_linux.go`: `os.ReadFile` of `/proc/stat` and `/proc/meminfo`. Cores are counted once at construction, falling back to `runtime.NumCPU`.
  - `probe_darwin.go`:
    - Load `libSystem.B.dylib` with `purego.Dlopen`. Resolve each symbol with `purego.Dlsym` and bind it with `purego.RegisterFunc`, so a missing symbol returns an error instead of panicking.
    - CPU: `host_statistics(HOST_CPU_LOAD_INFO=3, count 4)` with busy = user + system + nice.
    - Memory: `host_statistics64(HOST_VM_INFO64=4, count 38)`.
    - Page size: `vm_kernel_page_size` read through Dlsym, which stays correct under Rosetta.
    - Total memory: `unix.SysctlUint64("hw.memsize")`. Cores: `unix.SysctlUint32("hw.logicalcpu")`.
    - Call `mach_host_self()` **once** at construction and cache it; every call adds a send-right reference.
    - If setup fails, each method returns the setup error.
  - `disk_unix.go` (`darwin || linux`): `unix.Statfs` on the state dir.
  - `probe_other.go` (`!darwin && !linux`): every method returns `errors.ErrUnsupported`; cores = `runtime.NumCPU()`.
- **Routes.** `daemon.WithMachineStats(m)` and `web.WithMachineStats(m)` take a small interface `{ Sample() machine.Stats }`, defined where it is used. One Sampler is shared: the service builds it, the daemon holds it, and `StartWeb` passes it on, so both routes share the cache and the baseline. If no sampler is wired, the route returns 503 `machine_stats_unavailable`.

## Steps
1. **Sampler core.** Files: `internal/machine/{machine.go,sampler.go,sampler_test.go}`. Write the failing tests first, using a fake probe and a fake clock:
   - First sample: percent null, cores set, memory and disk filled.
   - +2s with Δbusy 50 / Δtotal 200 gives 25.
   - +0.5s returns the cache (one probe call, same `sampled_at`); at exactly +1s it re-samples.
   - Baseline 61s old gives null, and the next sample works against the new baseline; exactly 60s still gives a value.
   - A counter going down, or Δtotal 0, gives null.
   - A CPU error gives null percent but keeps cores and the baseline.
   - A memory or disk error makes only that section null.
   - Concurrent callers wait on the in-flight call (detected via `onWait`) and the probe runs once.
   - A repeated identical error logs once; recovery logs once.

   Verify: `go test -race ./internal/machine/`.
2. **Pure parsers and maths.** Files: `procfs.go`, `calc.go`, plus their tests. Failing table tests first:
   - `/proc/stat`: a full 10-field line with guest excluded, an old 4-field line, a missing `cpu` line (error), and counting `cpuN` lines.
   - `meminfo`: with `MemAvailable`; without it (fallback to MemFree + Buffers + Cached); `MemTotal` missing (error); available greater than total (clamped to 0); malformed lines ignored; kB converted to bytes.
   - `diskFromStatfs` and `darwinUsed` (normal and clamped cases).
3. **Linux probe.** `probe_linux.go` and `disk_unix.go`, plus `probe_linux_test.go` (Linux only): a live read gives total > 0, used ≤ total, and ticks that don't go down. Verify with `CGO_ENABLED=0 GOOS=linux go vet ./internal/machine/`. If a container runtime is available, also run `CGO_ENABLED=0 go test ./internal/machine/` inside a `golang:1.25` container. If not, say so and record it as deferred.
4. **Darwin probe.** Add purego with `go get github.com/ebitengine/purego@v0.11.1` (or the newest tag that keeps go 1.25.x). `git diff go.mod` must show no change to the `go` line. Add `probe_darwin.go` and `probe_darwin_test.go`:
   - Struct size 152, `internal_page_count` at offset 140.
   - Live read: total equals `hw.memsize`, 0 < used ≤ total, ticks don't go down, cores equals `hw.logicalcpu`.

   Verify with **both** `go test -race ./internal/machine/` and `CGO_ENABLED=0 go test ./internal/machine/`.
5. **Other platforms.** `probe_other.go`. Verify: `GOOS=freebsd go vet ./internal/machine/` and `GOOS=windows go vet ./internal/machine/`.
6. **Feature flag.** First update the hard-coded lists in `internal/observe/clamp_test.go:68` and `internal/observe/httpapi/httpapi_test.go:122` to end with `"machine_stats"`, which makes them fail. Then add `FeatureMachineStats` to `observe/event.go:51-81`. Verify: `go test -race ./internal/observe/...`.
7. **Socket route.** `GET /machine` in `daemon/server.go:254+` and the handler in `handlers_observe.go` (`writeData`), plus `WithMachineStats`. Failing test first in `internal/daemon`: 200 with exactly the keys `sampled_at`, `cpu`, `memory`, `disk`; a failing fake gives literal nulls; no sampler gives 503.
8. **Web route and wiring.**
   - Register `GET /api/v1/machine` on `apiMux` next to `/api/v1/state` (`web/web.go:698`), with no `requireOperatorToken`. Add `web.WithMachineStats`; `StartWeb` adds it to `observeOpts` (`daemon/server.go:462-489`).
   - In `service/process.go:1024`, add `daemon.WithMachineStats(machine.NewSampler(machine.NewProbe(filepath.Dir(sockPath))))`.
   - Failing tests first: the JSON shape test, and an auth-parity table against `/api/v1/state` using `newTokenSplitServer`/`requestAs` and `newClientTokenServer`. No token gives 401; operator and agent tokens give 200; a client token gives 403.
9. **CHANGELOG.** Under `## [Unreleased]` → `### Added`: "**Machine stats (`machine_stats`).** `GET /machine` (socket) and `GET /api/v1/machine` (same auth as `/api/v1/state`) report CPU percent and cores, memory used/total, and disk used/total for the state dir. Sampled on request and cached for 1s, with no subprocesses; a section that can't be read is `null`. Advertised as the `machine_stats` feature on the events hello."
10. **Verification gate** (orchestrator, before review).
    - Builds:
      - `CGO_ENABLED=0 GOOS=darwin GOARCH={arm64,amd64} go build ./cmd/leo`
      - `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./cmd/leo`
      - `GOOS=linux go list -deps ./cmd/leo | grep purego` must print nothing.
    - Live test on an isolated daemon:
      - Build with `CGO_ENABLED=0 go build -o bin/leo ./cmd/leo`; `go version -m bin/leo` should show `CGO_ENABLED=0`.
      - Create home `/tmp/leomach/` with a `leo.yaml` (web on 127.0.0.1:8371, one template). Start it with Bash `run_in_background` as `env LEO_HOME=/tmp/leomach ./bin/leo service --supervised --config /tmp/leomach/leo.yaml`.
      - Never run `leo service restart`. Tell Evan the test daemon strips production's roster lines.
    - Curl checks:
      - `curl --unix-socket /tmp/leomach/state/leo.sock http://leo/machine` twice, 2s apart: null percent first, a number second.
      - The web route with `Bearer $(cat /tmp/leomach/state/api.token)` returns 200; without a token, 401. Calling it within 1s of the socket route returns the same `sampled_at`.
      - The `/events` hello includes `machine_stats`.
    - Sanity checks, in the shell only:
      - Memory should match `vm_stat`'s (anonymous − purgeable + wired + compressor) × page size within a few percent.
      - Disk should match `df -k /tmp/leomach/state`.
    - Teardown: `pkill -f "service --supervised --config /tmp/leomach"`, then `trash /tmp/leomach`.
    - Check the load average, then run `make test` and `make lint`. `make e2e` isn't needed because no argv or config changed.
    - Finally, dispatch a `review` of `main...HEAD`.

## Orchestrator decisions
- All eight ambiguities are resolved as the planner assumed. The `{"ok":true,"data":…}` envelope is reported to leoterm.
- The implementer runs steps 1–9 and the build and live-curl parts of step 10. The orchestrator runs the review.
