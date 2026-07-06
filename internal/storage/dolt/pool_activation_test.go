package dolt

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestMaybeActivatePooler_NoOpWhenDisabled verifies the pooler stays OFF (and
// never rewrites ServerSocket) unless every opt-in condition holds.
func TestMaybeActivatePooler_NoOpWhenDisabled(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		cfg  Config
	}{
		{
			name: "env unset",
			env:  map[string]string{"BEADS_DOLT_POOL": ""},
			cfg:  Config{ServerHost: "127.0.0.1", ServerPort: 3307},
		},
		{
			name: "env false",
			env:  map[string]string{"BEADS_DOLT_POOL": "0"},
			cfg:  Config{ServerHost: "127.0.0.1", ServerPort: 3307},
		},
		{
			name: "test mode blocks activation",
			env:  map[string]string{"BEADS_DOLT_POOL": "1", "BEADS_TEST_MODE": "1"},
			cfg:  Config{ServerHost: "127.0.0.1", ServerPort: 3307},
		},
		{
			name: "explicit socket respected",
			env:  map[string]string{"BEADS_DOLT_POOL": "1"},
			cfg:  Config{ServerSocket: "/tmp/operator.sock", ServerHost: "127.0.0.1", ServerPort: 3307},
		},
		{
			name: "proxied server not hijacked",
			env:  map[string]string{"BEADS_DOLT_POOL": "1"},
			cfg:  Config{ProxiedServer: true, ServerHost: "127.0.0.1", ServerPort: 3307},
		},
		{
			name: "no port resolved",
			env:  map[string]string{"BEADS_DOLT_POOL": "1"},
			cfg:  Config{ServerHost: "127.0.0.1", ServerPort: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			before := tc.cfg.ServerSocket
			cfg := tc.cfg
			maybeActivatePooler(&cfg)
			if cfg.ServerSocket != before {
				t.Fatalf("ServerSocket changed from %q to %q; expected no-op", before, cfg.ServerSocket)
			}
		})
	}
}

// TestPoolerRootDir verifies the per-endpoint shared root dir is stable and
// keyed by host:port so all rigs fronting the same Dolt server converge.
func TestPoolerRootDir(t *testing.T) {
	a, err := poolerRootDir("127.0.0.1", 3307)
	if err != nil {
		t.Fatalf("poolerRootDir: %v", err)
	}
	b, err := poolerRootDir("127.0.0.1", 3307)
	if err != nil {
		t.Fatalf("poolerRootDir: %v", err)
	}
	if a != b {
		t.Fatalf("root dir not stable: %q vs %q", a, b)
	}
	if !strings.HasSuffix(a, filepath.Join("pool", "127.0.0.1_3307")) {
		t.Fatalf("unexpected root dir %q", a)
	}
	// Different port -> different dir (so two Dolt endpoints get two poolers).
	c, _ := poolerRootDir("127.0.0.1", 3308)
	if c == a {
		t.Fatalf("different ports collided: %q", c)
	}
}

func TestSanitizeKey(t *testing.T) {
	if got := sanitizeKey("127.0.0.1"); got != "127.0.0.1" {
		t.Errorf("sanitizeKey(127.0.0.1) = %q", got)
	}
	if got := sanitizeKey("my/evil:host"); got != "my_evil_host" {
		t.Errorf("sanitizeKey path-injection = %q, want my_evil_host", got)
	}
}
