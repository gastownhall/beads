package main

import (
	"os"
	"testing"
)

func TestStdioProbeFM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe fm: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe fm: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
