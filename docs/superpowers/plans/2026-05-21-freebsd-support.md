# FreeBSD Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add FreeBSD 15 process monitoring to this process-exporter fork using sysctl/kinfo_proc, maintaining full metric parity for core metrics (CPU, memory, I/O, FDs, threads, context switches, state, wchan).

**Architecture:** Split `proc/read.go` into shared types (`read.go`) and platform-specific implementations (`read_linux.go`, `read_freebsd.go`) using Go build tags. The FreeBSD implementation parses raw `kinfo_proc` structs from `sysctl` calls. Everything above the `Proc` interface (tracker, grouper, collector, main) stays unchanged.

**Tech Stack:** Go 1.26, `golang.org/x/sys/unix` for sysctl/Getrlimit, `encoding/binary` for struct parsing, FreeBSD 15 `kinfo_proc` struct (1088 bytes on amd64).

**Spec:** `docs/superpowers/specs/2026-05-21-freebsd-support-design.md`

**Environment:** This is a FreeBSD 15 amd64 machine. Go is installed. Linux tests cannot run natively but can be cross-compiled with `GOOS=linux`.

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `proc/read.go` | Modify | Shared types, interfaces, iterator, utility methods. Remove all `procfs` imports and Linux implementation |
| `proc/read_linux.go` | Create | Linux implementation moved from `read.go`, with `//go:build linux` tag |
| `proc/read_freebsd.go` | Create | FreeBSD implementation using sysctl/kinfo_proc, with `//go:build freebsd` tag |
| `proc/read_test.go` | Modify | Keep only shared test helpers and `TestIterator` |
| `proc/read_linux_test.go` | Create | Linux-specific tests moved from `read_test.go`, with `//go:build linux` tag |
| `proc/read_freebsd_test.go` | Create | FreeBSD-specific tests, with `//go:build freebsd` tag |
| `go.mod` | Modify | Promote `golang.org/x/sys` from indirect to direct dependency |
| `.goreleaser.yml` | Modify | Add `freebsd` to `goos` list |

**Files unchanged:** `proc/tracker.go`, `proc/grouper.go`, `proc/base_test.go`, `proc/tracker_test.go`, `proc/grouper_test.go`, `collector/process_collector.go`, `cmd/process-exporter/main.go`, `Makefile`

---

### Task 1: Split read.go into shared types and Linux implementation

**Files:**
- Modify: `proc/read.go`
- Create: `proc/read_linux.go`

This is a pure refactor. No behavior changes.

- [ ] **Step 1: Create `proc/read_linux.go` with all Linux-specific code**

```go
//go:build linux

package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/prometheus/procfs"
)

// See https://github.com/prometheus/procfs/blob/master/proc_stat.go for details on userHZ.
const userHZ = 100

type (
	// proccache implements the Proc interface by acting as wrapper for procfs.Proc
	// that caches results of some reads.
	proccache struct {
		procfs.Proc
		procid  *ID
		stat    *procfs.ProcStat
		status  *procfs.ProcStatus
		cmdline []string
		cgroups []procfs.Cgroup
		io      *procfs.ProcIO
		fs      *FS
		wchan   *string
	}

	proc struct {
		proccache
	}

	// procfsprocs implements procs using procfs.
	procfsprocs struct {
		Procs []procfs.Proc
		fs    *FS
	}

	// FS implements Source.
	FS struct {
		procfs.FS
		BootTime    uint64
		MountPoint  string
		GatherSMaps bool
		debug       bool
	}
)

func (p *proccache) GetPid() int {
	return p.Proc.PID
}

func (p *proccache) getStat() (procfs.ProcStat, error) {
	if p.stat == nil {
		stat, err := p.Proc.NewStat()
		if err != nil {
			return procfs.ProcStat{}, err
		}
		p.stat = &stat
	}

	return *p.stat, nil
}

func (p *proccache) getStatus() (procfs.ProcStatus, error) {
	if p.status == nil {
		status, err := p.Proc.NewStatus()
		if err != nil {
			return procfs.ProcStatus{}, err
		}
		p.status = &status
	}

	return *p.status, nil
}

func (p *proccache) getCgroups() ([]procfs.Cgroup, error) {
	if p.cgroups == nil {
		cgroups, err := p.Proc.Cgroups()
		if err != nil {
			return nil, err
		}
		p.cgroups = cgroups
	}

	return p.cgroups, nil
}

// GetProcID implements Proc.
func (p *proccache) GetProcID() (ID, error) {
	if p.procid == nil {
		stat, err := p.getStat()
		if err != nil {
			return ID{}, err
		}
		p.procid = &ID{Pid: p.GetPid(), StartTimeRel: stat.Starttime}
	}

	return *p.procid, nil
}

func (p *proccache) getCmdLine() ([]string, error) {
	if p.cmdline == nil {
		cmdline, err := p.Proc.CmdLine()
		if err != nil {
			return nil, err
		}
		p.cmdline = cmdline
	}
	return p.cmdline, nil
}

func (p *proccache) getWchan() (string, error) {
	if p.wchan == nil {
		wchan, err := p.Proc.Wchan()
		if err != nil {
			return "", err
		}
		p.wchan = &wchan
	}
	return *p.wchan, nil
}

func (p *proccache) getIo() (procfs.ProcIO, error) {
	if p.io == nil {
		io, err := p.Proc.IO()
		if err != nil {
			return procfs.ProcIO{}, err
		}
		p.io = &io
	}
	return *p.io, nil
}

// GetStatic returns the ProcStatic corresponding to this proc.
func (p *proccache) GetStatic() (Static, error) {
	// /proc/<pid>/cmdline is normally world-readable.
	cmdline, err := p.getCmdLine()
	if err != nil {
		return Static{}, err
	}

	// /proc/<pid>/stat is normally world-readable.
	stat, err := p.getStat()
	if err != nil {
		return Static{}, err
	}
	startTime := time.Unix(int64(p.fs.BootTime), 0).UTC()
	startTime = startTime.Add(time.Second / userHZ * time.Duration(stat.Starttime))

	// /proc/<pid>/status is normally world-readable.
	status, err := p.getStatus()
	if err != nil {
		return Static{}, err
	}

	// /proc/<pid>/cgroup(s) is normally world-readable.
	// However cgroups aren't always supported -> return an empty array in that
	// case.
	cgroups, err := p.getCgroups()
	var cgroupsStr []string
	if err != nil {
		cgroupsStr = []string{}
	} else {
		for _, c := range cgroups {
			cgroupsStr = append(cgroupsStr, c.Path)
		}
	}

	return Static{
		Name:         stat.Comm,
		Cmdline:      cmdline,
		Cgroups:      cgroupsStr,
		ParentPid:    stat.PPID,
		StartTime:    startTime,
		EffectiveUID: int(status.UIDs[1]),
	}, nil
}

func (p proc) GetCounts() (Counts, int, error) {
	stat, err := p.getStat()
	if err != nil {
		if err == os.ErrNotExist {
			err = ErrProcNotExist
		}
		return Counts{}, 0, fmt.Errorf("error reading stat file: %v", err)
	}

	status, err := p.getStatus()
	if err != nil {
		if err == os.ErrNotExist {
			err = ErrProcNotExist
		}
		return Counts{}, 0, fmt.Errorf("error reading status file: %v", err)
	}

	io, err := p.getIo()
	softerrors := 0
	if err != nil {
		softerrors++
	}
	return Counts{
		CPUUserTime:           float64(stat.UTime) / userHZ,
		CPUSystemTime:         float64(stat.STime) / userHZ,
		ReadBytes:             io.ReadBytes,
		WriteBytes:            io.WriteBytes,
		MajorPageFaults:       uint64(stat.MajFlt),
		MinorPageFaults:       uint64(stat.MinFlt),
		CtxSwitchVoluntary:    uint64(status.VoluntaryCtxtSwitches),
		CtxSwitchNonvoluntary: uint64(status.NonVoluntaryCtxtSwitches),
	}, softerrors, nil
}

func (p proc) GetWchan() (string, error) {
	return p.getWchan()
}

func (p proc) GetStates() (States, error) {
	stat, err := p.getStat()
	if err != nil {
		return States{}, err
	}

	var s States
	switch stat.State {
	case "R":
		s.Running++
	case "S":
		s.Sleeping++
	case "D":
		s.Waiting++
	case "Z":
		s.Zombie++
	default:
		s.Other++
	}
	return s, nil
}

// GetMetrics returns the current metrics for the proc.  The results are
// not cached.
func (p proc) GetMetrics() (Metrics, int, error) {
	counts, softerrors, err := p.GetCounts()
	if err != nil {
		return Metrics{}, 0, err
	}

	// We don't need to check for error here because p will have cached
	// the successful result of calling getStat in GetCounts.
	// Since GetMetrics isn't a pointer receiver method, our callers
	// won't see the effect of the caching between calls.
	stat, _ := p.getStat()

	// Ditto for states
	states, _ := p.GetStates()

	// Ditto for status
	status, _ := p.getStatus()

	numfds, err := p.Proc.FileDescriptorsLen()
	if err != nil {
		numfds = -1
		softerrors |= 1
	}

	limits, err := p.Proc.NewLimits()
	if err != nil {
		return Metrics{}, 0, err
	}

	wchan, err := p.getWchan()
	if err != nil {
		softerrors |= 1
	}

	memory := Memory{
		ResidentBytes: uint64(stat.ResidentMemory()),
		VirtualBytes:  uint64(stat.VirtualMemory()),
		VmSwapBytes:   uint64(status.VmSwap),
	}

	if p.proccache.fs.GatherSMaps {
		smaps, err := p.Proc.ProcSMapsRollup()
		if err != nil {
			softerrors |= 1
		} else {
			memory.ProportionalBytes = smaps.Pss
			memory.ProportionalSwapBytes = smaps.SwapPss
		}
	}

	return Metrics{
		Counts: counts,
		Memory: memory,
		Filedesc: Filedesc{
			Open:  int64(numfds),
			Limit: uint64(limits.OpenFiles),
		},
		NumThreads: uint64(stat.NumThreads),
		States:     states,
		Wchan:      wchan,
	}, softerrors, nil
}

func (p proc) GetThreads() ([]Thread, error) {
	fs, err := p.fs.threadFs(p.PID)
	if err != nil {
		return nil, err
	}

	threads := []Thread{}
	iter := fs.AllProcs()
	for iter.Next() {
		var id ID
		id, err = iter.GetProcID()
		if err != nil {
			continue
		}

		var static Static
		static, err = iter.GetStatic()
		if err != nil {
			continue
		}

		var counts Counts
		counts, _, err = iter.GetCounts()
		if err != nil {
			continue
		}

		wchan, _ := iter.GetWchan()
		states, _ := iter.GetStates()

		threads = append(threads, Thread{
			ThreadID:   ThreadID(id),
			ThreadName: static.Name,
			Counts:     counts,
			Wchan:      wchan,
			States:     states,
		})
	}
	err = iter.Close()
	if err != nil {
		return nil, err
	}
	if len(threads) < 2 {
		return nil, nil
	}

	return threads, nil
}

// NewFS returns a new FS mounted under the given mountPoint. It will error
// if the mount point can't be read.
func NewFS(mountPoint string, debug bool) (*FS, error) {
	fs, err := procfs.NewFS(mountPoint)
	if err != nil {
		return nil, err
	}
	stat, err := fs.NewStat()
	if err != nil {
		return nil, err
	}
	return &FS{fs, stat.BootTime, mountPoint, false, debug}, nil
}

func (fs *FS) threadFs(pid int) (*FS, error) {
	mountPoint := filepath.Join(fs.MountPoint, strconv.Itoa(pid), "task")
	tfs, err := procfs.NewFS(mountPoint)
	if err != nil {
		return nil, err
	}
	return &FS{tfs, fs.BootTime, mountPoint, fs.GatherSMaps, false}, nil
}

// AllProcs implements Source.
func (fs *FS) AllProcs() Iter {
	procs, err := fs.FS.AllProcs()
	if err != nil {
		err = fmt.Errorf("Error reading procs: %v", err)
	}
	return &procIterator{procs: procfsprocs{procs, fs}, err: err, idx: -1}
}

// get implements procs.
func (p procfsprocs) get(i int) Proc {
	return &proc{proccache{Proc: p.Procs[i], fs: p.fs}}
}

// length implements procs.
func (p procfsprocs) length() int {
	return len(p.Procs)
}
```

- [ ] **Step 2: Modify `proc/read.go` to keep only shared code**

Remove everything that was moved to `read_linux.go`. The file should contain only:

```go
package proc

import (
	"fmt"
	"time"
)

// ErrProcNotExist indicates a process couldn't be read because it doesn't exist,
// typically because it disappeared while we were reading it.
var ErrProcNotExist = fmt.Errorf("process does not exist")

type (
	// ID uniquely identifies a process.
	ID struct {
		// UNIX process id
		Pid int
		// The time the process started after system boot, the value is expressed
		// in clock ticks.
		StartTimeRel uint64
	}

	ThreadID ID

	// Static contains data read from /proc/pid/*
	Static struct {
		Name         string
		Cmdline      []string
		Cgroups      []string
		ParentPid    int
		StartTime    time.Time
		EffectiveUID int
	}

	// Counts are metric counters common to threads and processes and groups.
	Counts struct {
		CPUUserTime           float64
		CPUSystemTime         float64
		ReadBytes             uint64
		WriteBytes            uint64
		MajorPageFaults       uint64
		MinorPageFaults       uint64
		CtxSwitchVoluntary    uint64
		CtxSwitchNonvoluntary uint64
	}

	// Memory describes a proc's memory usage.
	Memory struct {
		ResidentBytes         uint64
		VirtualBytes          uint64
		VmSwapBytes           uint64
		ProportionalBytes     uint64
		ProportionalSwapBytes uint64
	}

	// Filedesc describes a proc's file descriptor usage and soft limit.
	Filedesc struct {
		// Open is the count of open file descriptors, -1 if unknown.
		Open int64
		// Limit is the fd soft limit for the process.
		Limit uint64
	}

	// States counts how many threads are in each state.
	States struct {
		Running  int
		Sleeping int
		Waiting  int
		Zombie   int
		Other    int
	}

	// Metrics contains data read from /proc/pid/*
	Metrics struct {
		Counts
		Memory
		Filedesc
		NumThreads uint64
		States
		Wchan string
	}

	// Thread contains per-thread data.
	Thread struct {
		ThreadID
		ThreadName string
		Counts
		Wchan string
		States
	}

	// IDInfo groups all info for a single process.
	IDInfo struct {
		ID
		Static
		Metrics
		Threads []Thread
	}

	// Proc wraps the details of the underlying procfs-reading library.
	// Any of these methods may fail if the process has disapeared.
	// We try to return as much as possible rather than an error, e.g.
	// if some /proc files are unreadable.
	Proc interface {
		// GetPid() returns the POSIX PID (process id).  They may be reused over time.
		GetPid() int
		// GetProcID() returns (pid,starttime), which can be considered a unique process id.
		GetProcID() (ID, error)
		// GetStatic() returns various details read from files under /proc/<pid>/.  Technically
		// name may not be static, but we'll pretend it is.
		GetStatic() (Static, error)
		// GetMetrics() returns various metrics read from files under /proc/<pid>/.
		// It returns an error on complete failure.  Otherwise, it returns metrics
		// and 0 on complete success, 1 if some (like I/O) couldn't be read.
		GetMetrics() (Metrics, int, error)
		GetStates() (States, error)
		GetWchan() (string, error)
		GetCounts() (Counts, int, error)
		GetThreads() ([]Thread, error)
	}

	// procs is a fancier []Proc that saves on some copying.
	procs interface {
		get(int) Proc
		length() int
	}

	// Iter is an iterator over a sequence of procs.
	Iter interface {
		// Next returns true if the iterator is not exhausted.
		Next() bool
		// Close releases any resources the iterator uses.
		Close() error
		// The iterator satisfies the Proc interface.
		Proc
	}

	// Source is a source of procs.
	Source interface {
		// AllProcs returns all the processes in this source at this moment in time.
		AllProcs() Iter
	}

	// procIterator implements the Iter interface
	procIterator struct {
		// procs is the list of Proc we're iterating over.
		procs
		// idx is the current iteration, i.e. it's an index into procs.
		idx int
		// err is set with an error when Next() fails.  It is not affected by failures accessing
		// the current iteration variable, e.g. with GetProcId.
		err error
		// Proc is the current iteration variable, or nil if Next() has never been called or the
		// iterator is exhausted.
		Proc
	}
)

func (ii IDInfo) String() string {
	return fmt.Sprintf("%+v:%+v", ii.ID, ii.Static)
}

// Add adds c2 to the counts.
func (c *Counts) Add(c2 Delta) {
	c.CPUUserTime += c2.CPUUserTime
	c.CPUSystemTime += c2.CPUSystemTime
	c.ReadBytes += c2.ReadBytes
	c.WriteBytes += c2.WriteBytes
	c.MajorPageFaults += c2.MajorPageFaults
	c.MinorPageFaults += c2.MinorPageFaults
	c.CtxSwitchVoluntary += c2.CtxSwitchVoluntary
	c.CtxSwitchNonvoluntary += c2.CtxSwitchNonvoluntary
}

// Sub subtracts c2 from the counts.
func (c Counts) Sub(c2 Counts) Delta {
	c.CPUUserTime -= c2.CPUUserTime
	c.CPUSystemTime -= c2.CPUSystemTime
	c.ReadBytes -= c2.ReadBytes
	c.WriteBytes -= c2.WriteBytes
	c.MajorPageFaults -= c2.MajorPageFaults
	c.MinorPageFaults -= c2.MinorPageFaults
	c.CtxSwitchVoluntary -= c2.CtxSwitchVoluntary
	c.CtxSwitchNonvoluntary -= c2.CtxSwitchNonvoluntary
	return Delta(c)
}

func (s *States) Add(s2 States) {
	s.Other += s2.Other
	s.Running += s2.Running
	s.Sleeping += s2.Sleeping
	s.Waiting += s2.Waiting
	s.Zombie += s2.Zombie
}

func (p IDInfo) GetThreads() ([]Thread, error) {
	return p.Threads, nil
}

// GetPid implements Proc.
func (p IDInfo) GetPid() int {
	return p.ID.Pid
}

// GetProcID implements Proc.
func (p IDInfo) GetProcID() (ID, error) {
	return p.ID, nil
}

// GetStatic implements Proc.
func (p IDInfo) GetStatic() (Static, error) {
	return p.Static, nil
}

// GetCounts implements Proc.
func (p IDInfo) GetCounts() (Counts, int, error) {
	return p.Metrics.Counts, 0, nil
}

// GetMetrics implements Proc.
func (p IDInfo) GetMetrics() (Metrics, int, error) {
	return p.Metrics, 0, nil
}

// GetStates implements Proc.
func (p IDInfo) GetStates() (States, error) {
	return p.States, nil
}

func (p IDInfo) GetWchan() (string, error) {
	return p.Wchan, nil
}

// Next implements Iter.
func (pi *procIterator) Next() bool {
	pi.idx++
	if pi.idx < pi.procs.length() {
		pi.Proc = pi.procs.get(pi.idx)
	} else {
		pi.Proc = nil
	}
	return pi.idx < pi.procs.length()
}

// Close implements Iter.
func (pi *procIterator) Close() error {
	pi.Next()
	pi.procs = nil
	pi.Proc = nil
	return pi.err
}
```

- [ ] **Step 3: Verify Linux cross-compilation**

Run: `GOOS=linux go build ./...`
Expected: successful build with no errors

Run: `GOOS=linux go vet ./...`
Expected: no issues

- [ ] **Step 4: Commit**

```bash
git add proc/read.go proc/read_linux.go
git commit -m "refactor: split proc/read.go into shared types and Linux implementation

Separate shared types/interfaces (read.go) from Linux-specific
implementation (read_linux.go) with build tags, preparing for
FreeBSD support."
```

---

### Task 2: Split read_test.go into shared and Linux-specific tests

**Files:**
- Modify: `proc/read_test.go`
- Create: `proc/read_linux_test.go`

- [ ] **Step 1: Create `proc/read_linux_test.go` with Linux-specific tests**

```go
//go:build linux

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func allprocs(procpath string) Iter {
	fs, err := NewFS(procpath, false)
	if err != nil {
		cwd, _ := os.Getwd()
		panic("can't read " + procpath + ", cwd=" + cwd + ", err=" + fmt.Sprintf("%v", err))
	}
	return fs.AllProcs()
}

func TestReadFixture(t *testing.T) {
	procs := allprocs("../fixtures")
	var pii IDInfo

	count := 0
	for procs.Next() {
		count++
		var err error
		pii, err = procinfo(procs)
		noerr(t, err)
	}
	err := procs.Close()
	noerr(t, err)
	if count != 1 {
		t.Fatalf("got %d procs, want 1", count)
	}

	wantprocid := ID{Pid: 14804, StartTimeRel: 0x4f27b}
	if diff := cmp.Diff(pii.ID, wantprocid); diff != "" {
		t.Errorf("procid differs: (-got +want)\n%s", diff)
	}

	stime, _ := time.Parse(time.RFC3339Nano, "2017-10-19T22:52:51.19Z")
	wantstatic := Static{
		Name:         "process-exporte",
		Cmdline:      []string{"./process-exporter", "-procnames", "bash"},
		Cgroups:      []string{"/system.slice/docker-8dde0b0d6e919baef8d635cd9399b22639ed1e400eaec1b1cb94ff3b216cf3c3.scope"},
		ParentPid:    10884,
		StartTime:    stime,
		EffectiveUID: 1000,
	}
	if diff := cmp.Diff(pii.Static, wantstatic); diff != "" {
		t.Errorf("static differs: (-got +want)\n%s", diff)
	}

	wantmetrics := Metrics{
		Counts: Counts{
			CPUUserTime:           0.1,
			CPUSystemTime:         0.04,
			ReadBytes:             1814455,
			WriteBytes:            0,
			MajorPageFaults:       0x2ff,
			MinorPageFaults:       0x643,
			CtxSwitchVoluntary:    72,
			CtxSwitchNonvoluntary: 6,
		},
		Memory: Memory{
			ResidentBytes: 0x7b1000,
			VirtualBytes:  0x1061000,
			VmSwapBytes:   0x2800,
		},
		Filedesc: Filedesc{
			Open:  5,
			Limit: 0x400,
		},
		NumThreads: 7,
		States:     States{Sleeping: 1},
	}
	if diff := cmp.Diff(pii.Metrics, wantmetrics); diff != "" {
		t.Errorf("metrics differs: (-got +want)\n%s", diff)
	}
}

// Basic test of proc reading: does AllProcs return at least two procs, one of which is us.
func TestAllProcs(t *testing.T) {
	procs := allprocs("/proc")
	count := 0
	for procs.Next() {
		count++
		if procs.GetPid() != os.Getpid() {
			continue
		}
		procid, err := procs.GetProcID()
		noerr(t, err)
		if procid.Pid != os.Getpid() {
			t.Errorf("got %d, want %d", procid.Pid, os.Getpid())
		}
		static, err := procs.GetStatic()
		noerr(t, err)
		if static.ParentPid != os.Getppid() {
			t.Errorf("got %d, want %d", static.ParentPid, os.Getppid())
		}
		metrics, _, err := procs.GetMetrics()
		noerr(t, err)
		if metrics.ResidentBytes == 0 {
			t.Errorf("got 0 bytes resident, want nonzero")
		}
		// All Go programs have multiple threads.
		if metrics.NumThreads < 2 {
			t.Errorf("got %d threads, want >1", metrics.NumThreads)
		}
		var zstates States
		if metrics.States == zstates {
			t.Errorf("got empty states")
		}
		threads, err := procs.GetThreads()
		if len(threads) < 2 {
			t.Errorf("got %d thread details, want >1", len(threads))
		}
	}
	err := procs.Close()
	noerr(t, err)
	if count == 0 {
		t.Errorf("got %d, want 0", count)
	}
}

// Test that we can observe the absence of a child process before it spawns and after it exits,
// and its presence during its lifetime.
func TestAllProcsSpawn(t *testing.T) {
	childprocs := func() []IDInfo {
		found := []IDInfo{}
		procs := allprocs("/proc")
		mypid := os.Getpid()
		for procs.Next() {
			procid, err := procs.GetProcID()
			if err != nil {
				continue
			}
			static, err := procs.GetStatic()
			if err != nil {
				continue
			}
			if static.ParentPid == mypid {
				found = append(found, IDInfo{procid, static, Metrics{}, nil})
			}
		}
		err := procs.Close()
		if err != nil {
			t.Fatalf("error closing procs iterator: %v", err)
		}
		return found
	}

	foundcat := func(procs []IDInfo) bool {
		for _, proc := range procs {
			if proc.Name == "cat" {
				return true
			}
		}
		return false
	}

	if foundcat(childprocs()) {
		t.Errorf("found cat before spawning it")
	}

	cmd := exec.Command("/bin/cat")
	wc, err := cmd.StdinPipe()
	noerr(t, err)
	err = cmd.Start()
	noerr(t, err)

	if !foundcat(childprocs()) {
		t.Errorf("didn't find cat after spawning it")
	}

	err = wc.Close()
	noerr(t, err)
	err = cmd.Wait()
	noerr(t, err)

	if foundcat(childprocs()) {
		t.Errorf("found cat after exit")
	}
}
```

- [ ] **Step 2: Modify `proc/read_test.go` to keep only shared code**

```go
package proc

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

type (
	// procIDInfos implements procs using a slice of already
	// populated ProcIdInfo.  Used for testing.
	procIDInfos []IDInfo
)

func (p procIDInfos) get(i int) Proc {
	return &p[i]
}

func (p procIDInfos) length() int {
	return len(p)
}

func procInfoIter(ps ...IDInfo) *procIterator {
	return &procIterator{procs: procIDInfos(ps), idx: -1}
}

func noerr(t *testing.T, err error) {
	if err != nil {
		t.Fatalf("error: %v", err)
	}
}

func TestIterator(t *testing.T) {
	p1 := newProc(1, "p1", Metrics{})
	p2 := newProc(2, "p2", Metrics{})
	want := []IDInfo{p1, p2}
	pis := procInfoIter(want...)
	got, err := consumeIter(pis)
	noerr(t, err)
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("procs differs: (-got +want)\n%s", diff)
	}
}
```

- [ ] **Step 3: Verify cross-compilation and shared tests**

Run: `GOOS=linux go build ./...`
Expected: successful build

Run: `go test ./proc/ -run TestIterator -v`
Expected: TestIterator passes (this test is platform-independent)

- [ ] **Step 4: Commit**

```bash
git add proc/read_test.go proc/read_linux_test.go
git commit -m "refactor: split proc/read_test.go into shared and Linux-specific tests

Move Linux-specific tests (TestReadFixture, TestAllProcs,
TestAllProcsSpawn) to read_linux_test.go with build tag.
Keep shared helpers and TestIterator in read_test.go."
```

---

### Task 3: Add golang.org/x/sys as direct dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Promote x/sys to direct dependency**

Run: `go get golang.org/x/sys`
Expected: go.mod updated, x/sys moves from indirect to direct

Run: `go mod tidy`
Expected: clean go.mod/go.sum

- [ ] **Step 2: Verify**

Run: `grep 'golang.org/x/sys' go.mod`
Expected: line without `// indirect` comment

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add golang.org/x/sys as direct dependency

Needed for FreeBSD sysctl access in the upcoming FreeBSD
process reader implementation."
```

---

### Task 4: Create FreeBSD process reader implementation

**Files:**
- Create: `proc/read_freebsd.go`

**Key references:**
- FreeBSD 15 amd64 `kinfo_proc` struct: 1088 bytes total
- Field offsets verified with C offsetof() on this machine:
  - ki_structsize: 0, ki_pid: 72, ki_ppid: 76, ki_uid: 168
  - ki_size: 256 (vsize bytes), ki_rssize: 264 (resident pages)
  - ki_start: 336 (Timeval), ki_stat: 388, ki_nice: 389
  - ki_tdname: 394 (17 bytes), ki_wmesg: 411 (9 bytes), ki_comm: 447 (20 bytes)
  - ki_numthreads: 596, ki_tid: 600, ki_rusage: 608 (144 bytes)
- Rusage offsets (from ki_rusage base):
  - Utime: 0 (16 bytes), Stime: 16 (16 bytes)
  - Minflt: 64, Majflt: 72, Inblock: 88, Oublock: 96
  - Nvcsw: 128, Nivcsw: 136
- Process states: SIDL=1, SRUN=2, SSLEEP=3, SSTOP=4, SZOMB=5, SWAIT=6, SLOCK=7
- Sysctl APIs verified working on this machine:
  - `unix.SysctlRaw("kern.proc.proc")` → all processes (no threads)
  - `unix.SysctlRaw("kern.proc.pid", pid)` → single process
  - `unix.SysctlRaw("kern.proc.args", pid)` → null-delimited cmdline
  - `unix.SysctlRaw("kern.proc.filedesc", pid)` → kinfo_file entries for FD count
  - Raw sysctl MIB `{1, 14, 0x11, pid}` → process threads (KERN_PROC_PID|KERN_PROC_INC_THREAD)

- [ ] **Step 1: Create `proc/read_freebsd.go`**

```go
//go:build freebsd

package proc

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// kinfo_proc field offsets for FreeBSD 15 amd64.
// Verified with C offsetof() on the target machine.
const (
	kpSize = 1088 // sizeof(struct kinfo_proc)

	kpOffPid        = 72
	kpOffPpid       = 76
	kpOffUid        = 168
	kpOffVsize      = 256 // ki_size: virtual memory in bytes
	kpOffRssize     = 264 // ki_rssize: resident set size in pages
	kpOffStart      = 336 // ki_start: Timeval (sec int64 + usec int64)
	kpOffStat       = 388 // ki_stat: process state byte
	kpOffTdname     = 394 // ki_tdname: thread name, 17 bytes
	kpOffTdnameLen  = 17
	kpOffWmesg      = 411 // ki_wmesg: wchan message, 9 bytes
	kpOffWmesgLen   = 9
	kpOffComm       = 447 // ki_comm: command name, 20 bytes
	kpOffCommLen    = 20
	kpOffNumthreads = 596
	kpOffTid        = 600
	kpOffRusage     = 608 // ki_rusage: struct rusage, 144 bytes
)

// Rusage field offsets relative to start of struct rusage.
const (
	ruOffUtime   = 0   // Timeval (16 bytes)
	ruOffStime   = 16  // Timeval (16 bytes)
	ruOffMinflt  = 64  // int64
	ruOffMajflt  = 72  // int64
	ruOffInblock = 88  // int64
	ruOffOublock = 96  // int64
	ruOffNvcsw   = 128 // int64
	ruOffNivcsw  = 136 // int64
)

// FreeBSD process states from sys/proc.h.
const (
	sIDL   = 1
	sRUN   = 2
	sSLEEP = 3
	sSTOP  = 4
	sZOMB  = 5
	sWAIT  = 6
	sLOCK  = 7
)

// devBsize is the block size used for I/O block count conversion.
const devBsize = 512

type (
	// FS implements Source for FreeBSD using sysctl.
	FS struct {
		MountPoint  string // accepted for interface compat, unused
		GatherSMaps bool   // accepted, ignored on FreeBSD
		debug       bool
		pageSize    uint64
	}

	// freebsdProc implements Proc by reading fields from a raw kinfo_proc.
	freebsdProc struct {
		raw        []byte // raw kinfo_proc bytes
		fs         *FS
		cmdline    []string
		hasCmdline bool
	}

	// freebsdProcs implements the procs interface for use with procIterator.
	freebsdProcs struct {
		entries [][]byte // each entry is a raw kinfo_proc
		fs      *FS
	}
)

// NewFS returns a new FS. The mountPoint argument is accepted for interface
// compatibility but is unused on FreeBSD (process data comes from sysctl).
func NewFS(mountPoint string, debug bool) (*FS, error) {
	return &FS{
		MountPoint: mountPoint,
		debug:      debug,
		pageSize:   uint64(os.Getpagesize()),
	}, nil
}

// AllProcs implements Source by reading all processes via sysctl kern.proc.proc.
func (fs *FS) AllProcs() Iter {
	buf, err := unix.SysctlRaw("kern.proc.proc")
	if err != nil {
		return &procIterator{err: fmt.Errorf("error reading procs: %v", err), idx: -1}
	}
	entries := parseKinfoProcs(buf)
	return &procIterator{procs: freebsdProcs{entries: entries, fs: fs}, err: nil, idx: -1}
}

// parseKinfoProcs splits a raw sysctl buffer into individual kinfo_proc entries.
// Each entry starts with a ki_structsize field (int32) giving its length.
func parseKinfoProcs(buf []byte) [][]byte {
	var result [][]byte
	offset := 0
	for offset+4 <= len(buf) {
		sz := int(binary.LittleEndian.Uint32(buf[offset:]))
		if sz == 0 || offset+sz > len(buf) {
			break
		}
		entry := make([]byte, sz)
		copy(entry, buf[offset:offset+sz])
		result = append(result, entry)
		offset += sz
	}
	return result
}

func (p freebsdProcs) get(i int) Proc {
	return &freebsdProc{raw: p.entries[i], fs: p.fs}
}

func (p freebsdProcs) length() int {
	return len(p.entries)
}

// cstring extracts a null-terminated string from a byte slice.
func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func (p *freebsdProc) GetPid() int {
	return int(int32(binary.LittleEndian.Uint32(p.raw[kpOffPid:])))
}

func (p *freebsdProc) GetProcID() (ID, error) {
	pid := p.GetPid()
	startSec := binary.LittleEndian.Uint64(p.raw[kpOffStart:])
	startUsec := binary.LittleEndian.Uint64(p.raw[kpOffStart+8:])
	// Combine into a unique uint64 for process identification.
	startTimeRel := startSec*1000000 + startUsec
	return ID{Pid: pid, StartTimeRel: startTimeRel}, nil
}

func (p *freebsdProc) getCmdline() ([]string, error) {
	if p.hasCmdline {
		return p.cmdline, nil
	}
	pid := p.GetPid()
	buf, err := unix.SysctlRaw("kern.proc.args", pid)
	if err != nil {
		if err == unix.ESRCH || err == unix.ENOENT {
			return nil, ErrProcNotExist
		}
		return nil, err
	}
	// kern.proc.args returns null-delimited arguments.
	s := strings.TrimRight(string(buf), "\x00")
	var args []string
	if s != "" {
		args = strings.Split(s, "\x00")
	}
	p.cmdline = args
	p.hasCmdline = true
	return p.cmdline, nil
}

func (p *freebsdProc) GetStatic() (Static, error) {
	cmdline, err := p.getCmdline()
	if err != nil {
		return Static{}, err
	}

	comm := cstring(p.raw[kpOffComm : kpOffComm+kpOffCommLen])
	ppid := int(int32(binary.LittleEndian.Uint32(p.raw[kpOffPpid:])))
	uid := int(binary.LittleEndian.Uint32(p.raw[kpOffUid:]))

	startSec := int64(binary.LittleEndian.Uint64(p.raw[kpOffStart:]))
	startUsec := int64(binary.LittleEndian.Uint64(p.raw[kpOffStart+8:]))
	startTime := time.Unix(startSec, startUsec*1000).UTC()

	return Static{
		Name:         comm,
		Cmdline:      cmdline,
		Cgroups:      []string{},
		ParentPid:    ppid,
		StartTime:    startTime,
		EffectiveUID: uid,
	}, nil
}

func (p *freebsdProc) GetCounts() (Counts, int, error) {
	ru := kpOffRusage

	utimeSec := int64(binary.LittleEndian.Uint64(p.raw[ru+ruOffUtime:]))
	utimeUsec := int64(binary.LittleEndian.Uint64(p.raw[ru+ruOffUtime+8:]))
	stimeSec := int64(binary.LittleEndian.Uint64(p.raw[ru+ruOffStime:]))
	stimeUsec := int64(binary.LittleEndian.Uint64(p.raw[ru+ruOffStime+8:]))

	return Counts{
		CPUUserTime:           float64(utimeSec) + float64(utimeUsec)/1e6,
		CPUSystemTime:         float64(stimeSec) + float64(stimeUsec)/1e6,
		ReadBytes:             binary.LittleEndian.Uint64(p.raw[ru+ruOffInblock:]) * devBsize,
		WriteBytes:            binary.LittleEndian.Uint64(p.raw[ru+ruOffOublock:]) * devBsize,
		MajorPageFaults:       binary.LittleEndian.Uint64(p.raw[ru+ruOffMajflt:]),
		MinorPageFaults:       binary.LittleEndian.Uint64(p.raw[ru+ruOffMinflt:]),
		CtxSwitchVoluntary:    binary.LittleEndian.Uint64(p.raw[ru+ruOffNvcsw:]),
		CtxSwitchNonvoluntary: binary.LittleEndian.Uint64(p.raw[ru+ruOffNivcsw:]),
	}, 0, nil
}

func (p *freebsdProc) GetStates() (States, error) {
	stat := p.raw[kpOffStat]
	var s States
	switch stat {
	case sRUN:
		s.Running++
	case sSLEEP:
		s.Sleeping++
	case sWAIT, sLOCK:
		s.Waiting++
	case sZOMB:
		s.Zombie++
	default:
		s.Other++
	}
	return s, nil
}

func (p *freebsdProc) GetWchan() (string, error) {
	return cstring(p.raw[kpOffWmesg : kpOffWmesg+kpOffWmesgLen]), nil
}

func (p *freebsdProc) GetMetrics() (Metrics, int, error) {
	counts, softerrors, err := p.GetCounts()
	if err != nil {
		return Metrics{}, 0, err
	}

	states, _ := p.GetStates()
	wchan, _ := p.GetWchan()

	vsize := binary.LittleEndian.Uint64(p.raw[kpOffVsize:])
	rsPages := binary.LittleEndian.Uint64(p.raw[kpOffRssize:])
	rss := rsPages * p.fs.pageSize

	pid := p.GetPid()
	numfds, err := countFDs(pid)
	if err != nil {
		numfds = -1
		softerrors |= 1
	}

	// FD limit: read our own rlimit as best-effort fallback.
	// Per-process rlimit for other processes requires root on FreeBSD.
	var fdLimit uint64
	var rlim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlim); err == nil {
		fdLimit = uint64(rlim.Cur)
	}

	numthreads := uint64(binary.LittleEndian.Uint32(p.raw[kpOffNumthreads:]))

	return Metrics{
		Counts: counts,
		Memory: Memory{
			ResidentBytes: rss,
			VirtualBytes:  vsize,
		},
		Filedesc: Filedesc{
			Open:  int64(numfds),
			Limit: fdLimit,
		},
		NumThreads: numthreads,
		States:     states,
		Wchan:      wchan,
	}, softerrors, nil
}

// countFDs returns the number of open file descriptors for a process
// by iterating kinfo_file entries from sysctl kern.proc.filedesc.
func countFDs(pid int) (int, error) {
	buf, err := unix.SysctlRaw("kern.proc.filedesc", pid)
	if err != nil {
		return -1, err
	}
	count := 0
	offset := 0
	for offset+4 <= len(buf) {
		sz := int(binary.LittleEndian.Uint32(buf[offset:]))
		if sz == 0 || offset+sz > len(buf) {
			break
		}
		count++
		offset += sz
	}
	return count, nil
}

func (p *freebsdProc) GetThreads() ([]Thread, error) {
	pid := p.GetPid()
	threadBuf, err := sysctlProcThreads(pid)
	if err != nil {
		return nil, err
	}

	entries := parseKinfoProcs(threadBuf)
	var threads []Thread
	for _, entry := range entries {
		tp := &freebsdProc{raw: entry, fs: p.fs}

		tid := int(int32(binary.LittleEndian.Uint32(entry[kpOffTid:])))
		tdname := cstring(entry[kpOffTdname : kpOffTdname+kpOffTdnameLen])

		// Use the process start time for thread ID stability.
		startSec := binary.LittleEndian.Uint64(entry[kpOffStart:])
		startUsec := binary.LittleEndian.Uint64(entry[kpOffStart+8:])
		startTimeRel := startSec*1000000 + startUsec

		counts, _, _ := tp.GetCounts()
		wchan, _ := tp.GetWchan()
		states, _ := tp.GetStates()

		threads = append(threads, Thread{
			ThreadID:   ThreadID(ID{Pid: tid, StartTimeRel: startTimeRel}),
			ThreadName: tdname,
			Counts:     counts,
			Wchan:      wchan,
			States:     states,
		})
	}

	if len(threads) < 2 {
		return nil, nil
	}
	return threads, nil
}

// sysctlProcThreads returns kinfo_proc entries for all threads of a process.
// Uses raw sysctl MIB: {CTL_KERN=1, KERN_PROC=14, KERN_PROC_PID|KERN_PROC_INC_THREAD=0x11, pid}
func sysctlProcThreads(pid int) ([]byte, error) {
	mib := [4]int32{1, 14, 0x11, int32(pid)}

	// First call: get required buffer size.
	n := uintptr(0)
	_, _, errno := unix.Syscall6(
		unix.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		4,
		0,
		uintptr(unsafe.Pointer(&n)),
		0, 0,
	)
	if errno != 0 {
		return nil, fmt.Errorf("sysctl size query for pid %d threads: %w", pid, errno)
	}
	if n == 0 {
		return nil, nil
	}

	// Second call: read the data.
	buf := make([]byte, n)
	_, _, errno = unix.Syscall6(
		unix.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		4,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
		0, 0,
	)
	if errno != 0 {
		return nil, fmt.Errorf("sysctl read for pid %d threads: %w", pid, errno)
	}

	return buf[:n], nil
}
```

- [ ] **Step 2: Verify FreeBSD build**

Run: `go build ./...`
Expected: successful build on FreeBSD

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 3: Commit**

```bash
git add proc/read_freebsd.go
git commit -m "feat: add FreeBSD process reader implementation

Implements the Proc and Source interfaces for FreeBSD using
sysctl and kinfo_proc struct parsing. Supports all core metrics:
CPU, memory, I/O (block-based), FDs, threads, context switches,
process state, wchan, and start time."
```

---

### Task 5: Create FreeBSD tests and verify

**Files:**
- Create: `proc/read_freebsd_test.go`

- [ ] **Step 1: Create `proc/read_freebsd_test.go`**

```go
//go:build freebsd

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func allprocs(procpath string) Iter {
	fs, err := NewFS(procpath, false)
	if err != nil {
		cwd, _ := os.Getwd()
		panic("can't create FS, cwd=" + cwd + ", err=" + fmt.Sprintf("%v", err))
	}
	return fs.AllProcs()
}

// TestAllProcs verifies that AllProcs returns processes including our own,
// and that metrics can be read for our process.
func TestAllProcs(t *testing.T) {
	procs := allprocs("")
	count := 0
	foundSelf := false
	for procs.Next() {
		count++
		if procs.GetPid() != os.Getpid() {
			continue
		}
		foundSelf = true

		procid, err := procs.GetProcID()
		noerr(t, err)
		if procid.Pid != os.Getpid() {
			t.Errorf("got pid %d, want %d", procid.Pid, os.Getpid())
		}
		if procid.StartTimeRel == 0 {
			t.Errorf("got zero StartTimeRel")
		}

		static, err := procs.GetStatic()
		noerr(t, err)
		if static.ParentPid != os.Getppid() {
			t.Errorf("got ppid %d, want %d", static.ParentPid, os.Getppid())
		}
		if static.Name == "" {
			t.Errorf("got empty process name")
		}
		if len(static.Cmdline) == 0 {
			t.Errorf("got empty cmdline")
		}

		metrics, _, err := procs.GetMetrics()
		noerr(t, err)
		if metrics.ResidentBytes == 0 {
			t.Errorf("got 0 bytes resident, want nonzero")
		}
		if metrics.VirtualBytes == 0 {
			t.Errorf("got 0 bytes virtual, want nonzero")
		}
		// All Go programs have multiple threads.
		if metrics.NumThreads < 2 {
			t.Errorf("got %d threads, want >1", metrics.NumThreads)
		}
		var zstates States
		if metrics.States == zstates {
			t.Errorf("got empty states")
		}
		if metrics.Filedesc.Open < 1 {
			t.Errorf("got %d open fds, want >0", metrics.Filedesc.Open)
		}

		threads, err := procs.GetThreads()
		noerr(t, err)
		if len(threads) < 2 {
			t.Errorf("got %d thread details, want >1", len(threads))
		}
	}
	err := procs.Close()
	noerr(t, err)
	if count < 2 {
		t.Errorf("got %d procs, want >1", count)
	}
	if !foundSelf {
		t.Errorf("did not find own process in AllProcs")
	}
}

// TestAllProcsSpawn verifies we can observe child process creation and exit.
func TestAllProcsSpawn(t *testing.T) {
	childprocs := func() []IDInfo {
		found := []IDInfo{}
		procs := allprocs("")
		mypid := os.Getpid()
		for procs.Next() {
			procid, err := procs.GetProcID()
			if err != nil {
				continue
			}
			static, err := procs.GetStatic()
			if err != nil {
				continue
			}
			if static.ParentPid == mypid {
				found = append(found, IDInfo{procid, static, Metrics{}, nil})
			}
		}
		err := procs.Close()
		if err != nil {
			t.Fatalf("error closing procs iterator: %v", err)
		}
		return found
	}

	foundcat := func(procs []IDInfo) bool {
		for _, proc := range procs {
			if proc.Name == "cat" {
				return true
			}
		}
		return false
	}

	if foundcat(childprocs()) {
		t.Errorf("found cat before spawning it")
	}

	cmd := exec.Command("/bin/cat")
	wc, err := cmd.StdinPipe()
	noerr(t, err)
	err = cmd.Start()
	noerr(t, err)

	if !foundcat(childprocs()) {
		t.Errorf("didn't find cat after spawning it")
	}

	err = wc.Close()
	noerr(t, err)
	err = cmd.Wait()
	noerr(t, err)

	if foundcat(childprocs()) {
		t.Errorf("found cat after exit")
	}
}

// TestCPUMetrics verifies CPU time is being read correctly by
// burning some CPU and checking the metric increases.
func TestCPUMetrics(t *testing.T) {
	fs, err := NewFS("", false)
	noerr(t, err)

	getMyMetrics := func() Metrics {
		iter := fs.AllProcs()
		for iter.Next() {
			if iter.GetPid() != os.Getpid() {
				continue
			}
			m, _, err := iter.GetMetrics()
			noerr(t, err)
			return m
		}
		t.Fatal("did not find own process")
		return Metrics{}
	}

	before := getMyMetrics()

	// Burn some CPU.
	sum := 0.0
	for i := 0; i < 10_000_000; i++ {
		sum += float64(i) * 0.001
	}
	_ = sum

	after := getMyMetrics()
	cpuDelta := (after.CPUUserTime + after.CPUSystemTime) -
		(before.CPUUserTime + before.CPUSystemTime)
	if cpuDelta <= 0 {
		t.Errorf("CPU time did not increase after work: before=%f+%f after=%f+%f",
			before.CPUUserTime, before.CPUSystemTime,
			after.CPUUserTime, after.CPUSystemTime)
	}
}
```

- [ ] **Step 2: Run FreeBSD tests**

Run: `go test ./proc/ -v -count=1`
Expected: all tests pass (TestIterator, TestAllProcs, TestAllProcsSpawn, TestCPUMetrics, plus tracker/grouper tests)

- [ ] **Step 3: Run all tests**

Run: `go test ./... -count=1`
Expected: all tests pass across all packages

- [ ] **Step 4: Commit**

```bash
git add proc/read_freebsd_test.go
git commit -m "test: add FreeBSD process reader tests

Tests AllProcs, process spawn/exit tracking, and CPU metric
accuracy on FreeBSD."
```

---

### Task 6: Update goreleaser config and final verification

**Files:**
- Modify: `.goreleaser.yml`

- [ ] **Step 1: Add FreeBSD to goreleaser goos**

In `.goreleaser.yml`, change the `goos` list from:
```yaml
    goos:
      - linux
```
to:
```yaml
    goos:
      - linux
      - freebsd
```

- [ ] **Step 2: Verify FreeBSD native build**

Run: `CGO_ENABLED=0 go build -o /tmp/process-exporter ./cmd/process-exporter/`
Expected: binary built successfully

Run: `/tmp/process-exporter -version`
Expected: prints version info

- [ ] **Step 3: Verify Linux cross-compilation still works**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/process-exporter/`
Expected: successful cross-compile

- [ ] **Step 4: Smoke test on FreeBSD**

Run: `/tmp/process-exporter -once-to-stdout-delay 1s -procnames "$(basename $(which go))" 2>/dev/null | head -30`
Expected: Prometheus metrics output with namedprocess_namegroup_* lines showing non-zero values

- [ ] **Step 5: Commit**

```bash
git add .goreleaser.yml
git commit -m "build: add FreeBSD to goreleaser targets"
```

- [ ] **Step 6: Clean up temp files**

Run: `rm -f /tmp/process-exporter /tmp/structsize /tmp/structsize.c /tmp/kf_size /tmp/kf_size.c /tmp/testproc.go /tmp/testproc2.go`
