package main

import (
	"os"
	"testing"
)

func TestStdioProbeLM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe lm: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe lm: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
