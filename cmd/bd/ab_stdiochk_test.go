package main

// Probe instrumentation for gastownhall/beads#5881 (DO NOT MERGE).
//
// stdioChk prints whether os.Stdout / os.Stderr still point at the objects
// captured at package var-init (realStdout / realStderr, zz_stdio_leak_guard_test.go).
// testMainInner calls it at labeled checkpoints so a single CI run bisects
// WHERE in the TestMain window the swap happens. Output goes to the entry-time
// genuine stderr so it is visible regardless of the current swap state.

import (
	"fmt"
	"os"
)

func stdioChk(label string) {
	fmt.Fprintf(realStderr, "STDIOCHK %-22s stdout_ok=%v stderr_ok=%v (stderr fd=%d name=%q)\n",
		label, os.Stdout == realStdout, os.Stderr == realStderr, os.Stderr.Fd(), os.Stderr.Name())
}
