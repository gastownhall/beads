package main

import (
	"os"
	"testing"
)

func TestStdioProbeRM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe rm: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe rm: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
