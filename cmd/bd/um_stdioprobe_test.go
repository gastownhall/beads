package main

import (
	"os"
	"testing"
)

func TestStdioProbeUM(t *testing.T) {
	if os.Stdout != realStdout {
		t.Errorf("probe um: os.Stdout already polluted (fd=%d name=%q)", os.Stdout.Fd(), os.Stdout.Name())
	}
	if os.Stderr != realStderr {
		t.Errorf("probe um: os.Stderr already polluted (fd=%d name=%q)", os.Stderr.Fd(), os.Stderr.Name())
	}
}
