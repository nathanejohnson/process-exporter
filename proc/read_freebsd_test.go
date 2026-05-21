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
