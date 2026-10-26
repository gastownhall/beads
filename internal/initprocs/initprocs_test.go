package initprocs

import (
	"os"
	"runtime"
	"testing"
)

func TestDoneRestoresGOMAXPROCS(t *testing.T) {
	want := restore
	if want == 0 {
		t.Skipf("GOMAXPROCS was already at most %d; init left it alone", initProcs)
	}
	if got := runtime.GOMAXPROCS(0); got != initProcs {
		t.Fatalf("GOMAXPROCS before Done = %d, want the init cap %d", got, initProcs)
	}
	Done()
	if got := runtime.GOMAXPROCS(0); got != want {
		t.Fatalf("GOMAXPROCS after Done = %d, want %d", got, want)
	}
	Done()
	if got := runtime.GOMAXPROCS(0); got != want {
		t.Fatalf("a second Done changed GOMAXPROCS to %d", got)
	}
}

// TestDoneHonorsAnExplicitGOMAXPROCS tells the two hand-backs apart: with
// GOMAXPROCS set, Done restores that exact value; without it, Done hands
// GOMAXPROCS back to the runtime's default, which keeps tracking the CPU limit.
func TestDoneHonorsAnExplicitGOMAXPROCS(t *testing.T) {
	saved, savedProcs, savedDefault := restore, setProcs, setDefault
	t.Cleanup(func() { restore, setProcs, setDefault = saved, savedProcs, savedDefault })
	var procs []int
	defaults := 0
	setProcs = func(n int) int { procs = append(procs, n); return n }
	setDefault = func() { defaults++ }

	t.Setenv("GOMAXPROCS", "7")
	restore = 7
	Done()
	if len(procs) != 1 || procs[0] != 7 || defaults != 0 {
		t.Fatalf("GOMAXPROCS=7: GOMAXPROCS calls %v, default calls %d; want GOMAXPROCS(7) alone", procs, defaults)
	}

	procs = nil
	if err := os.Unsetenv("GOMAXPROCS"); err != nil {
		t.Fatal(err)
	}
	restore = 192
	Done()
	if len(procs) != 0 || defaults != 1 {
		t.Fatalf("GOMAXPROCS unset: GOMAXPROCS calls %v, default calls %d; want SetDefaultGOMAXPROCS alone", procs, defaults)
	}
}
