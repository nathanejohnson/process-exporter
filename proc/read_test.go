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
