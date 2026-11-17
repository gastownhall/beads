//go:build darwin

package doltserver

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func withProcessReads(t *testing.T, pids func(int) ([]int, error), parents func() (map[int]int, error)) {
	t.Helper()
	origPIDs, origParents := listeningPIDs, processParents
	listeningPIDs, processParents = pids, parents
	t.Cleanup(func() { listeningPIDs, processParents = origPIDs, origParents })
}

func TestListenerOwnership_FakeReads(t *testing.T) {
	self := os.Getpid()
	tree := map[int]int{self: 1, 501: self, 502: 501, 600: 1, 700: 700}
	cases := []struct {
		name       string
		pids       []int
		pidsErr    error
		parents    map[int]int
		parentsErr error
		provable   bool
		ownership  Ownership
		owned      bool
		known      bool
	}{
		{name: "child holds the listener", pids: []int{self}, parents: tree, ownership: OwnershipOwned, owned: true, known: true},
		{name: "grandchild holds the listener (wrapper)", pids: []int{502}, parents: tree, ownership: OwnershipOwned, owned: true, known: true},
		{name: "foreign process holds it", pids: []int{600}, parents: tree, ownership: OwnershipForeign, owned: false, known: true},
		{name: "foreign and own both listed", pids: []int{600, 501}, parents: tree, ownership: OwnershipOwned, owned: true, known: true},
		{name: "holder missing from the snapshot", pids: []int{999}, parents: tree, ownership: OwnershipForeign, owned: false, known: true},
		{name: "cyclic snapshot does not loop", pids: []int{700}, parents: tree, ownership: OwnershipForeign, owned: false, known: true},
		// lsof lists no listener although one answered: another user's
		// socket, or a forwarded one. Where lsof provably lists this user's
		// own listeners that is not the child's; where it cannot (no
		// working lsof) server mode falls back.
		{name: "no listener listed, ownership provable", pids: nil, parents: tree, provable: true, ownership: OwnershipUnlisted, owned: false, known: true},
		{name: "no listener listed, ownership not provable", pids: nil, parents: tree, ownership: OwnershipUnlisted, owned: false, known: false},
		{name: "lsof fails", pidsErr: errors.New("lsof: not found"), parents: tree, ownership: OwnershipUnknown, owned: false, known: false},
		{name: "ps fails", pids: []int{self}, parentsErr: errors.New("ps: not found"), ownership: OwnershipUnknown, owned: false, known: false},
		{name: "empty snapshot", pids: []int{self}, parents: map[int]int{}, ownership: OwnershipUnknown, owned: false, known: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withProcessReads(t,
				func(int) ([]int, error) { return tc.pids, tc.pidsErr },
				func() (map[int]int, error) { return tc.parents, tc.parentsErr },
			)
			origProvable := listenerOwnershipProvable
			listenerOwnershipProvable = func() bool { return tc.provable }
			t.Cleanup(func() { listenerOwnershipProvable = origProvable })
			if got := classifyListener(self, 1); got != tc.ownership {
				t.Errorf("classifyListener = %d, want %d", got, tc.ownership)
			}
			owned, known := listenerOwnership(self, 1)
			if owned != tc.owned || known != tc.known {
				t.Errorf("listenerOwnership = (%v, %v), want (%v, %v)", owned, known, tc.owned, tc.known)
			}
		})
	}
}

func TestListenerOwnership_DeadChildIsKnown(t *testing.T) {
	withProcessReads(t,
		func(int) ([]int, error) { t.Fatal("listener read on a dead child"); return nil, nil },
		func() (map[int]int, error) { t.Fatal("tree read on a dead child"); return nil, nil },
	)
	if owned, known := listenerOwnership(1<<30, 1); owned || !known {
		t.Errorf("listenerOwnership(no such pid) = (%v, %v), want (false, true)", owned, known)
	}
}

func TestParseLsofPIDs(t *testing.T) {
	got := parseLsofPIDs([]byte("12245\n\n  77360 \nnot-a-pid\n0\n"))
	want := []int{12245, 77360}
	if len(got) != len(want) {
		t.Fatalf("parseLsofPIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseLsofPIDs = %v, want %v", got, want)
		}
	}
}

func TestParsePSParents(t *testing.T) {
	got := parsePSParents([]byte("    1     0\n  501     1\n12245   501\nbad line here\n  77360 x\n"))
	want := map[int]int{1: 0, 501: 1, 12245: 501}
	if len(got) != len(want) {
		t.Fatalf("parsePSParents = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parsePSParents[%d] = %d, want %d", k, got[k], v)
		}
	}
}

// TestListenerOwnership_RealTools mirrors the Linux real-/proc test: our own
// in-process listener is ours, a pid that does not exist owns nothing, a
// sibling process's listener is not ours, and a listener in a child process
// we spawn is ours through the tree.
func TestListenerOwnership_RealTools(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not on PATH")
	}
	port := foreignGreeter(t)
	if owned, known := listenerOwnership(os.Getpid(), port); !owned || !known {
		t.Errorf("listenerOwnership(self, own listener) = (%v, %v), want (true, true)", owned, known)
	}
	if owned, known := listenerOwnership(1<<30, port); owned || !known {
		t.Errorf("listenerOwnership(no such pid) = (%v, %v), want (false, true)", owned, known)
	}

	// A child that holds its own listener: nc -l on a fresh port, under a
	// shell that does not exec it, so the listener is a grandchild.
	ncPort := freeLoopbackPort(t)
	cmd := exec.Command("sh", "-c", "nc -l 127.0.0.1 "+strconv.Itoa(ncPort)+"; exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start nc under sh: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, err := listeningPIDs(ncPort)
		if err == nil && len(pids) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("nc never listened within 5s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if owned, known := listenerOwnership(cmd.Process.Pid, ncPort); !owned || !known {
		t.Errorf("listenerOwnership(sh child, grandchild nc listener) = (%v, %v), want (true, true)", owned, known)
	}
	if owned, known := listenerOwnership(os.Getpid(), ncPort); !owned || !known {
		t.Errorf("listenerOwnership(self, descendant nc listener) = (%v, %v), want (true, true)", owned, known)
	}
	// The child does not own its parent's listener: the tree is walked up
	// from the holder, never down from the pid being asked about.
	if owned, known := listenerOwnership(cmd.Process.Pid, port); owned || !known {
		t.Errorf("listenerOwnership(sh child, parent's listener) = (%v, %v), want (false, true)", owned, known)
	}
}

// TestListenerOwnershipProvable pins the host probe cmd/bd uses to choose
// the generated proxied config's log level: true where lsof and ps show this
// process's own listener, and false where they cannot answer, so such a host
// keeps dolt's ready line as proof.
func TestListenerOwnershipProvable(t *testing.T) {
	t.Run("tools cannot answer", func(t *testing.T) {
		withProcessReads(t,
			func(int) ([]int, error) { return nil, errors.New("lsof: not found") },
			func() (map[int]int, error) { return nil, errors.New("ps: not found") },
		)
		if ListenerOwnershipProvable() {
			t.Error("ListenerOwnershipProvable() = true without lsof and ps, want false")
		}
	})
	t.Run("real tools", func(t *testing.T) {
		if _, err := exec.LookPath("lsof"); err != nil {
			t.Skip("lsof not on PATH")
		}
		if !ListenerOwnershipProvable() {
			t.Error("ListenerOwnershipProvable() = false with lsof and ps, want true")
		}
	})
}
