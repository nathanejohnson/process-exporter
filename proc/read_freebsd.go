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
		return &procIterator{procs: freebsdProcs{fs: fs}, err: fmt.Errorf("error reading procs: %v", err), idx: -1}
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
		if sz < kpSize || offset+sz > len(buf) {
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
// Entries with kf_fd < 0 are special (CWD, root, text vnode) and excluded.
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
		// kf_fd is at offset 8 in kinfo_file; negative values are
		// special entries (KF_FD_TYPE_CWD, KF_FD_TYPE_ROOT, etc.).
		if offset+12 <= len(buf) {
			kfFd := int32(binary.LittleEndian.Uint32(buf[offset+8:]))
			if kfFd >= 0 {
				count++
			}
		}
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

	// Second call: read the data. Retry with a larger buffer if the
	// thread count grew between the size query and the data read.
	for retries := 0; retries < 3; retries++ {
		buf := make([]byte, n)
		_, _, errno = unix.Syscall6(
			unix.SYS___SYSCTL,
			uintptr(unsafe.Pointer(&mib[0])),
			4,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&n)),
			0, 0,
		)
		if errno == unix.ENOMEM {
			n *= 2
			continue
		}
		if errno != 0 {
			return nil, fmt.Errorf("sysctl read for pid %d threads: %w", pid, errno)
		}
		return buf[:n], nil
	}
	return nil, fmt.Errorf("sysctl read for pid %d threads: buffer too small after retries", pid)
}
