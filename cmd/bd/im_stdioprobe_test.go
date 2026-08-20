package main

import (
	"os"
	"testing"
)

func TestStdioProbeIM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe im: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe im: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
