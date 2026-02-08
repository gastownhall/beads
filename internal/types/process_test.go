package types

import (
	"os"
	"testing"
)

func TestIsProcessAlive(t *testing.T) {
	currentHost, err := os.Hostname()
	if err != nil {
		t.Fatalf("failed to get hostname: %v", err)
	}

	tests := []struct {
		name     string
		pid      int
		hostname string
		want     bool
	}{
		{
			name:     "current process (should be alive)",
			pid:      os.Getpid(),
			hostname: currentHost,
			want:     true,
		},

		{
			name:     "different hostname (assume alive)",
			pid:      12345,
			hostname: "remote-host-xyz",
			want:     true,
		},
		{
			name:     "current process on different hostname (assume alive)",
			pid:      os.Getpid(),
			hostname: "remote-host-xyz",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsProcessAlive(tt.pid, tt.hostname)
			if got != tt.want {
				t.Errorf("IsProcessAlive(%d, %s) = %v, want %v", tt.pid, tt.hostname, got, tt.want)
			}
		})
	}
}

func TestIsProcessAlive_CurrentProcess(t *testing.T) {
	// Test that our own process is detected as alive
	currentHost, _ := os.Hostname()
	pid := os.Getpid()

	if !IsProcessAlive(pid, currentHost) {
		t.Error("current process should be detected as alive")
	}
}

func TestIsProcessAlive_RemoteHost(t *testing.T) {
	// Test that remote processes are assumed alive (can't verify)
	if !IsProcessAlive(12345, "some-remote-host") {
		t.Error("remote host processes should be assumed alive")
	}
}
