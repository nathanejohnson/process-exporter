# FreeBSD Support for process-exporter

## Goal

Add FreeBSD 15 support to this fork of process-exporter for production server monitoring. Core metrics (CPU, memory, I/O, FDs, threads, context switches, process state, start time, wchan) should work. Linux-only concepts (cgroups, smaps) are excluded.

## Constraints

- Pure Go only (`CGO_ENABLED=0`), no C dependencies
- FreeBSD 15 only — no need to support older versions
- Fork-only — not intended for upstream contribution
- Maintain zero changes to `tracker.go`, `grouper.go`, `collector/process_collector.go`, and `main.go`

## Approach: Platform-split `read.go`

Split `proc/read.go` into three files:

### `proc/read.go` (no build tags)

Shared types, interfaces, and utility methods:

- Types: `ID`, `Static`, `Counts`, `Memory`, `Filedesc`, `States`, `Metrics`, `Thread`, `IDInfo`, `Delta`
- Interfaces: `Proc`, `Source`, `Iter`, `procs`
- Iterator: `procIterator` and its `Next()`/`Close()` methods
- Utility methods: `Counts.Add`, `Counts.Sub`, `States.Add`, `IDInfo.Get*` methods

### `proc/read_linux.go` (`//go:build linux`)

Existing Linux implementation moved here unchanged:

- `proccache` struct (embeds `procfs.Proc`, caches stat/status/io/cmdline/cgroups)
- `proc` struct (wraps `proccache`)
- `procfsprocs` struct
- `FS` struct (embeds `procfs.FS`, holds `BootTime`, `MountPoint`, `GatherSMaps`)
- `NewFS(mountPoint string, debug bool) (*FS, error)`
- `FS.AllProcs() Iter`
- `FS.threadFs(pid int) (*FS, error)`
- All `proccache` methods: `getStat`, `getStatus`, `getCgroups`, `getCmdLine`, `getWchan`, `getIo`
- All `proc` methods: `GetCounts`, `GetMetrics`, `GetStates`, `GetWchan`, `GetThreads`
- `const userHZ = 100`

### `proc/read_freebsd.go` (`//go:build freebsd`)

New FreeBSD implementation using `golang.org/x/sys/unix`:

```go
type FS struct {
    MountPoint  string   // accepted but unused
    GatherSMaps bool     // accepted but ignored
    debug       bool
    pageSize    uint64   // from syscall.Getpagesize()
}

type freebsdProc struct {
    kinfo   unix.KinfoProc
    pid     int
    fs      *FS
    cmdline []string   // lazy, from kern.proc.args
    fds     *int       // lazy, from kern.proc.filedesc
}

// freebsdProcs implements the procs interface for use with the shared procIterator
type freebsdProcs struct {
    procs []unix.KinfoProc
    fs    *FS
}
```

Key functions:

- `NewFS(mountPoint string, debug bool) (*FS, error)` — ignores mountPoint, calls `Getpagesize()`
- `FS.AllProcs() Iter` — single `sysctl("kern.proc.all")` call, wraps results
- `freebsdProc.GetPid() int`
- `freebsdProc.GetProcID() (ID, error)` — uses `(pid, ki_start)` as unique ID, converting `ki_start` timeval to a uint64
- `freebsdProc.GetStatic() (Static, error)` — name from `ki_comm`, cmdline from `kern.proc.args.<pid>`, cgroups empty, start time from `ki_start`, UID from `ki_uid`, PPID from `ki_ppid`
- `freebsdProc.GetCounts() (Counts, int, error)` — CPU from `ki_rusage.Utime/Stime`, I/O from `Inblock/Oublock * 512`, faults from `Majflt/Minflt`, ctx switches from `Nvcsw/Nivcsw`
- `freebsdProc.GetMetrics() (Metrics, int, error)` — aggregates counts, memory, FDs, threads, state, wchan
- `freebsdProc.GetStates() (States, error)` — maps `ki_stat` (SRUN, SSLEEP, SWAIT, SZOMB, etc.) to `States`
- `freebsdProc.GetWchan() (string, error)` — from `ki_wmesg`
- `freebsdProc.GetThreads() ([]Thread, error)` — sysctl `kern.proc.pid.<pid>` with `KERN_PROC_INC_THREAD`, returns one `Thread` per kernel thread

## Metric Mapping

| Metric | Linux Source | FreeBSD Source | Notes |
|--------|-------------|----------------|-------|
| PID, PPID | `/proc/pid/stat` | `Ki_pid`, `Ki_ppid` | Direct |
| Process name | `/proc/pid/stat` (comm) | `Ki_comm` | Direct |
| Cmdline | `/proc/pid/cmdline` | `kern.proc.args.<pid>` | Direct |
| UID | `/proc/pid/status` | `Ki_uid` | Direct |
| CPU user time | utime / userHZ | `Ki_rusage.Utime` | Timeval, no tick conversion |
| CPU system time | stime / userHZ | `Ki_rusage.Stime` | Timeval, no tick conversion |
| Major page faults | `/proc/pid/stat` | `Ki_rusage.Majflt` | Direct |
| Minor page faults | `/proc/pid/stat` | `Ki_rusage.Minflt` | Direct |
| Ctx switches (vol) | `/proc/pid/status` | `Ki_rusage.Nvcsw` | Direct |
| Ctx switches (nonvol) | `/proc/pid/status` | `Ki_rusage.Nivcsw` | Direct |
| Resident memory | `/proc/pid/stat` (rss) | `Ki_rssize * pagesize` | Multiply by page size |
| Virtual memory | `/proc/pid/stat` (vsize) | `Ki_size` | Already bytes |
| VmSwap | `/proc/pid/status` | N/A | Report 0 |
| I/O read bytes | `/proc/pid/io` | `Ki_rusage.Inblock * 512` | Block count * DEV_BSIZE |
| I/O write bytes | `/proc/pid/io` | `Ki_rusage.Oublock * 512` | Block count * DEV_BSIZE |
| FD count | `/proc/pid/fd` count | `kern.proc.filedesc.<pid>` | Via sysctl |
| FD limit | `/proc/pid/limits` | `Getrlimit(RLIMIT_NOFILE)` | Own process only unless root |
| Thread count | `/proc/pid/stat` | `Ki_numthreads` | Direct |
| Process state | `/proc/pid/stat` | `Ki_stat` | SRUN->Running, SSLEEP->Sleeping, SWAIT->Waiting, SZOMB->Zombie, else->Other |
| Start time | boot_time + ticks | `Ki_start` | Absolute timeval |
| Wchan | `/proc/pid/wchan` | `Ki_wmesg` | String, not symbol |
| Threads detail | `/proc/pid/task/` | sysctl with `KERN_PROC_INC_THREAD` | One kinfo_proc per thread |
| Cgroups | `/proc/pid/cgroup` | N/A | Return empty slice |
| SMaps | `/proc/pid/smaps` | N/A | Flag ignored |

### Known approximations

- **I/O bytes**: FreeBSD provides block counts, not byte counts. Multiplying by 512 (DEV_BSIZE) is the standard approximation but won't match Linux's exact byte-level `/proc/pid/io` counters.
- **FD limit**: Only readable for the exporter's own process unless running as root. For other processes, report 0 as a soft error.
- **VmSwap**: No reliable per-process swap metric on FreeBSD. Always 0.

## Build and Release Changes

- **`.goreleaser.yml`**: Add `freebsd` to `goos`. Limit FreeBSD `goarch` to `amd64` and `arm64`.
- **`go.mod`**: Add `golang.org/x/sys` as a direct dependency (already indirect).
- **`main.go`**: No changes. `--procfs` and `--gather-smaps` flags accepted but effectively unused on FreeBSD.
- **Makefile**: No changes needed. `CGO_ENABLED=0 GOOS=freebsd go build` works.
- **CI**: No changes. Build and test on FreeBSD directly.

## Files Changed

| File | Action |
|------|--------|
| `proc/read.go` | Keep shared types/interfaces, remove Linux implementation |
| `proc/read_linux.go` | New file, moved Linux implementation with `//go:build linux` |
| `proc/read_freebsd.go` | New file, FreeBSD implementation with `//go:build freebsd` |
| `.goreleaser.yml` | Add `freebsd` to `goos` |
| `go.mod` | Add `golang.org/x/sys` as direct dependency |

## Files Unchanged

- `proc/tracker.go` — works through `Proc`/`Source` interfaces
- `proc/grouper.go` — works through `Tracker`
- `collector/process_collector.go` — works through `Source`/`Grouper`
- `cmd/process-exporter/main.go` — no conditional logic needed
- `Makefile` — `CGO_ENABLED=0` already set
