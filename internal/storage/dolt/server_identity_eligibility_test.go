package dolt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
)

// Server identity eligibility (GH#4637, AC-6): the gate is for endpoints bd
// manages. Everything else — external hosts bd never runs a server at,
// gateway routing, live sockets, TLS, caller-explicit ports, workspaces that
// declare their server external, and explicit operator disablement — must be
// exempt: no record read, no identity query, the open behaves exactly as
// before. An invalid knob value must never disable the gate.

// TestServerIdentityEligibilityExemptions is the eligibility table: each
// exempt configuration resolves to a nil expectation (verifyOpenServerIdentity
// no-ops on nil), and each managed configuration resolves to a real one.
func TestServerIdentityEligibilityExemptions(t *testing.T) {
	// A clean slate for the knobs: no env override, no config setting. The
	// port env vars matter because an ambient routing port classifies a
	// workspace as externally managed (ResolveServerMode check 2c), which
	// is an exemption in its own right — one this table must not inherit.
	t.Setenv("BEADS_DOLT_VERIFY_SERVER_IDENTITY", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_SHARED_SERVER_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	beadsDir := t.TempDir()
	base := &Config{
		BeadsDir:   beadsDir,
		Path:       filepath.Join(beadsDir, "dolt"),
		ServerHost: "127.0.0.1",
		ServerPort: 3319,
	}

	cases := []struct {
		name    string
		mutate  func(*Config)
		setenv  map[string]string
		wantNil bool
	}{
		{
			name:    "external host bd never runs a server at",
			mutate:  func(c *Config) { c.ServerHost = "db.example.com" },
			wantNil: true,
		},
		{
			name:    "TLS endpoint",
			mutate:  func(c *Config) { c.ServerTLS = true },
			wantNil: true,
		},
		{
			name:    "gateway credential command owns routing",
			mutate:  func(c *Config) { c.Gateway = true },
			wantNil: true,
		},
		{
			name:    "live unix socket",
			mutate:  func(c *Config) { c.ServerSocket = "/tmp/beads-test.sock" },
			wantNil: true,
		},
		{
			name: "caller-explicit port",
			mutate: func(c *Config) {
				c.ServerPortSource = doltserver.PortSourceCallerExplicit
			},
			wantNil: true,
		},
		{
			name:    "workspace declaring its server external",
			setenv:  map[string]string{"BEADS_DOLT_SERVER_MODE": "1"},
			wantNil: true,
		},
		{
			name: "operator disablement via env",
			setenv: map[string]string{
				"BEADS_DOLT_VERIFY_SERVER_IDENTITY": "0",
			},
			wantNil: true,
		},
		{
			name:    "invalid env value does not disable the gate",
			setenv:  map[string]string{"BEADS_DOLT_VERIFY_SERVER_IDENTITY": "not-a-boolean"},
			wantNil: false,
		},
		{
			name: "shared-server endpoint stays managed",
			mutate: func(c *Config) {
				c.ServerPortSharedServer = true
			},
			setenv:  map[string]string{"BEADS_DOLT_SHARED_SERVER": "1", "BEADS_SHARED_SERVER_DIR": t.TempDir()},
			wantNil: false,
		},
		{
			name:    "bd's own workspace server stays managed",
			wantNil: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.setenv {
				t.Setenv(k, v)
			}
			cfg := *base
			cfg.BeadsDir = t.TempDir()
			cfg.Path = filepath.Join(cfg.BeadsDir, "dolt")
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			got := serverIdentityExpectation(&cfg, cfg.BeadsDir)
			if tc.wantNil && got != nil {
				t.Fatalf("exempt endpoint must resolve a nil expectation (no record read, no identity query), got %+v", got)
			}
			if !tc.wantNil {
				if got == nil {
					t.Fatal("managed endpoint must resolve a real identity expectation")
				}
				if got.DataDir == "" || got.Host == "" || got.Port == 0 {
					t.Fatalf("managed expectation must name the data root and endpoint, got %+v", got)
				}
			}
		})
	}
}

// TestServerIdentityDisabledOpenPerformsNoGateOperations is the behavioral
// half: an open whose endpoint would be gated must run zero gate operations
// when verification is explicitly disabled, and exactly one when it is not.
func TestServerIdentityDisabledOpenPerformsNoGateOperations(t *testing.T) {
	skipIfNoServer(t)

	origVerify := verifyServerIdentity
	t.Cleanup(func() { verifyServerIdentity = origVerify })

	gateCalls := 0
	verifyServerIdentity = func(ctx context.Context, q doltversion.RowQuerier, exp doltserver.ServerIdentityExpectation) error {
		gateCalls++
		return origVerify(ctx, q, exp)
	}

	openShared := func(t *testing.T, dbName string) error {
		t.Helper()
		t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		assertDatabaseNotExists(t, testServerPort, dbName)
		t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })
		_, err := New(context.Background(), sharedIdentityConfig(t, dbName))
		return err
	}

	// Control: enabled, the gate runs once and refuses the unpaired server.
	t.Run("enabled gate runs", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_VERIFY_SERVER_IDENTITY", "")
		if err := openShared(t, fmt.Sprintf("test_identity_gate_on_%d", testServerPort)); !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
			t.Fatalf("expected the gate to refuse the unpaired shared server, got: %v", err)
		}
		if gateCalls != 1 {
			t.Fatalf("enabled open must run the gate exactly once, got %d call(s)", gateCalls)
		}
	})

	// Disabled: the same open runs zero gate operations and succeeds.
	t.Run("disabled gate is bypassed entirely", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_VERIFY_SERVER_IDENTITY", "0")
		gateCalls = 0
		if err := openShared(t, fmt.Sprintf("test_identity_gate_off_%d", testServerPort)); err != nil {
			t.Fatalf("disabled verification must preserve the pre-gate open behavior, got: %v", err)
		}
		if gateCalls != 0 {
			t.Fatalf("disabled open must perform no gate operation, got %d call(s)", gateCalls)
		}
	})

	// An invalid value is not an opt-out: the gate still runs.
	t.Run("invalid disable value does not disable", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_VERIFY_SERVER_IDENTITY", "not-a-boolean")
		gateCalls = 0
		if err := openShared(t, fmt.Sprintf("test_identity_gate_invalid_%d", testServerPort)); !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
			t.Fatalf("an invalid disable value must not disable the gate, got: %v", err)
		}
		if gateCalls != 1 {
			t.Fatalf("gate must still run with an invalid disable value, got %d call(s)", gateCalls)
		}
	})
}
