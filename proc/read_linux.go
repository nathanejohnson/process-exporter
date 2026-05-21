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

// See https://github.com/prometheus/procfs/blob/master/proc_stat.go for details on userHZ.
const userHZ = 100

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
