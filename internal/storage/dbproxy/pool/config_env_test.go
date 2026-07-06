package pool

import (
	"path/filepath"
	"testing"
)

func TestEnabledFromEnv(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "TRUE": true, "yes": true, "on": true, "T": true,
		"":      false,
		"0":     false,
		"false": false,
		"off":   false,
		"2":     false, // only explicit truthy tokens enable it
	}
	for v, want := range cases {
		t.Setenv(EnvEnabled, v)
		if got := EnabledFromEnv(); got != want {
			t.Errorf("EnabledFromEnv(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestSizeFromEnv(t *testing.T) {
	cases := map[string]int{
		"":    0,
		"0":   0,
		"-3":  0,
		"abc": 0,
		"8":   8,
		"32":  32,
	}
	for v, want := range cases {
		t.Setenv(EnvSize, v)
		if got := SizeFromEnv(); got != want {
			t.Errorf("SizeFromEnv(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestSocketFromEnv(t *testing.T) {
	t.Setenv(EnvSocket, "")
	if got, want := SocketFromEnv("/root/dir"), filepath.Join("/root/dir", PoolSocketName); got != want {
		t.Errorf("SocketFromEnv default = %q, want %q", got, want)
	}
	t.Setenv(EnvSocket, "/custom/p.sock")
	if got := SocketFromEnv("/root/dir"); got != "/custom/p.sock" {
		t.Errorf("SocketFromEnv override = %q, want /custom/p.sock", got)
	}
}
