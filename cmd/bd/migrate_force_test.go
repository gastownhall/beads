package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIsForcedMigrate covers the root-PersistentPreRunE helpers behind
// `bd migrate --force`: only the migrate and migrate-schema commands report
// forced, and --force conflicts with the preview flags (--dry-run/--inspect)
// because the gate-overridden store open would apply pending migrations
// before the preview ever ran.
func TestIsForcedMigrate(t *testing.T) {
	t.Cleanup(func() {
		_ = migrateCmd.Flags().Set("force", "false")
		_ = migrateCmd.Flags().Set("dry-run", "false")
		_ = migrateCmd.Flags().Set("inspect", "false")
		_ = migrateSchemaCmd.Flags().Set("force", "false")
	})
	set := func(name, v string) {
		t.Helper()
		if err := migrateCmd.Flags().Set(name, v); err != nil {
			t.Fatalf("set --%s=%s: %v", name, v, err)
		}
	}

	if isForcedMigrate(migrateCmd) {
		t.Error("force unset: want false")
	}
	set("force", "true")
	if !isForcedMigrate(migrateCmd) {
		t.Error("force set on migrate: want true")
	}
	if isForcedMigrate(rootCmd) {
		t.Error("non-migrate command: want false even while migrate's flag is set")
	}

	if got := forcedMigratePreviewFlag(migrateCmd); got != "" {
		t.Errorf("no preview flags set: want \"\", got %q", got)
	}
	set("dry-run", "true")
	if got := forcedMigratePreviewFlag(migrateCmd); got != "dry-run" {
		t.Errorf("dry-run set: want \"dry-run\", got %q", got)
	}
	set("dry-run", "false")
	set("inspect", "true")
	if got := forcedMigratePreviewFlag(migrateCmd); got != "inspect" {
		t.Errorf("inspect set: want \"inspect\", got %q", got)
	}

	if err := migrateSchemaCmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("set migrate schema --force: %v", err)
	}
	if !isForcedMigrate(migrateSchemaCmd) {
		t.Error("force set on migrate schema: want true")
	}
	if got := forcedMigratePreviewFlag(migrateSchemaCmd); got != "" {
		t.Errorf("migrate schema has no preview flags: want \"\", got %q", got)
	}
}

// TestForcedProxiedMigrateSchemaWarnsOnTheSanctionedBypass pins the second half
// of bd-n5e58c (GH#6656): --force lifting the typed refusal is not silent. The
// gate prints the generic close-co-resident-clients warning to stderr — once,
// before any provider work — because the proxy is a byte pump that cannot name
// the clients the operator has to stop. The bypass reads the flag's VALUE, so
// a set-but-false --force keeps both the refusal and the silence.
func TestForcedProxiedMigrateSchemaWarnsOnTheSanctionedBypass(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() {
		jsonOutput = oldJSON
		_ = migrateSchemaCmd.Flags().Set("force", "false")
	})

	for _, topo := range []ProxyTopology{
		ProxyTopologyManagedLocal, ProxyTopologyExternalTCP, ProxyTopologyExternalUnix,
		ProxyTopologyTeamServer, ProxyTopologyUnknown,
	} {
		t.Run(string(topo), func(t *testing.T) {
			force := migrateSchemaCmd.Flags().Lookup("force")
			if force == nil {
				t.Fatal("migrate schema registers no --force flag")
			}
			t.Cleanup(func() {
				_ = force.Value.Set(force.DefValue)
				force.Changed = false
			})

			// --force actually true: the refusal lifts and the warning is the
			// whole of stderr, exactly once.
			if err := force.Value.Set("true"); err != nil {
				t.Fatal(err)
			}
			var gateErr error
			stderr := captureStderr(t, func() {
				gateErr = validateProxyRegistryBeforeProvider(migrateSchemaCmd, topo)
			})
			if gateErr != nil {
				t.Fatalf("%s: forced migrate schema was still refused before the provider: %v", topo, gateErr)
			}
			if want := forcedProxiedMigrateSchemaWarning + "\n"; stderr != want {
				t.Errorf("%s: forced bypass stderr = %q, want exactly the one-line warning %q", topo, stderr, want)
			}

			// Set but false: the value, not the presence, decides — refusal
			// stays and the warning is never printed for one.
			if err := force.Value.Set("false"); err != nil {
				t.Fatal(err)
			}
			var refuseErr error
			stderr = captureStderr(t, func() {
				refuseErr = validateProxyRegistryBeforeProvider(migrateSchemaCmd, topo)
			})
			if refuseErr == nil {
				t.Fatalf("%s: a set-but-false --force lifted the refusal; the bypass reads the value", topo)
			}
			if strings.Contains(stderr, "close co-resident") {
				t.Errorf("%s: a refused invocation printed the force warning: %q", topo, stderr)
			}

			// Unset: same refusal, same silence — the default posture is
			// unchanged for operators who never typed the flag.
			force.Changed = false
			var unsetErr error
			stderr = captureStderr(t, func() {
				unsetErr = validateProxyRegistryBeforeProvider(migrateSchemaCmd, topo)
			})
			if unsetErr == nil {
				t.Fatalf("%s: an unset --force lifted the refusal", topo)
			}
			if strings.Contains(stderr, "close co-resident") {
				t.Errorf("%s: an unset flag printed the force warning: %q", topo, stderr)
			}
		})
	}
}

// TestMigrateSchemaRefusedOnProxiedServerWithoutForce pins bd-n5e58c (GH#6656):
// on a proxied-server workspace the provider open promotes the schema during
// provider open, underneath library clients the proxy cannot see, so
// `bd migrate schema` without --force must be refused, typed, by the
// pre-provider gate — on every proxied shape, including the unknown topology
// a corrupt sidecar produces (a workspace that cannot prove it owns its Dolt
// server does not get the privileges of one that can).
func TestMigrateSchemaRefusedOnProxiedServerWithoutForce(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() {
		jsonOutput = oldJSON
		_ = migrateSchemaCmd.Flags().Set("force", "false")
	})
	_ = migrateSchemaCmd.Flags().Set("force", "false")

	for _, topo := range []ProxyTopology{
		ProxyTopologyManagedLocal, ProxyTopologyExternalTCP, ProxyTopologyExternalUnix,
		ProxyTopologyTeamServer, ProxyTopologyUnknown,
	} {
		t.Run(string(topo), func(t *testing.T) {
			var gateErr error
			out := captureStdout(t, func() error {
				gateErr = validateProxyRegistryBeforeProvider(migrateSchemaCmd, topo)
				return nil
			})
			if gateErr == nil {
				t.Fatalf("%s: migrate schema without --force was permitted before provider open; want a typed refusal", topo)
			}
			if code, ok := exitCodeFromError(gateErr); !ok || code == 0 {
				t.Errorf("%s: refusal = %#v; want a typed non-zero exit", topo, gateErr)
			}
			// The validator renders through HandleProxyCapabilityError, so it
			// returns an exitError, never the ProxyCapabilityError itself; in
			// JSON mode the typed refusal is the one object on stdout.
			var refusal struct {
				Code    string `json:"code"`
				Error   string `json:"error"`
				Mutates bool   `json:"mutates"`
				Reason  string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(out), &refusal); err != nil {
				t.Fatalf("%s: refusal stdout is not one JSON object: %v\nstdout:\n%s", topo, err, out)
			}
			if refusal.Code != "proxy.migrate.unsupported" {
				t.Errorf("%s: refusal code = %q, want proxy.migrate.unsupported", topo, refusal.Code)
			}
			if refusal.Mutates {
				t.Errorf("%s: refusal reported mutates=true; a refusal never mutates", topo)
			}
			if refusal.Reason != "design" {
				t.Errorf("%s: refusal reason = %q, want design", topo, refusal.Reason)
			}
		})
	}
}
