package main

import (
	"os"
	"testing"
)

func TestStdioProbeOM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe om: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe om: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
