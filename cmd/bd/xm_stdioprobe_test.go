package main

import (
	"os"
	"testing"
)

func TestStdioProbeXM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe xm: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe xm: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
