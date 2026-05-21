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

	// Source is a source of procs.
	Source interface {
		// AllProcs returns all the processes in this source at this moment in time.
		AllProcs() Iter
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
