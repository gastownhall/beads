package main

import (
	"encoding/json"
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

// TestMigrateSchemaRefusedOnProxiedServerWithoutForce pins bd-n5e58c (GH#6656):
// on a proxied-server workspace the schema is promoted during provider open,
// underneath library clients the proxy cannot see, so `bd migrate schema`
// without --force must be refused, typed, by the pre-provider gate. It drives
// the gate through runProxyRegistryGate, which holds stdioMutex and captures
// the rendered refusal instead of letting it reach the test binary's stdout;
// TestProxyMigrateSchemaRefusedUnlessForced pins the refusal's words.
func TestMigrateSchemaRefusedOnProxiedServerWithoutForce(t *testing.T) {
	for _, topo := range allProxiedTopologies {
		t.Run(string(topo), func(t *testing.T) {
			setMigrateSchemaForce(t, "false")
			stdout, _, err := runProxyRegistryGate(t, migrateSchemaCmd, topo)
			if code, ok := exitCodeFromError(err); !ok || code != 1 {
				t.Fatalf("refusal = %#v; want a typed refusal exiting 1 before provider open", err)
			}
			var got refusalJSONForTest
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.Code != "proxy.migrate.unsupported" {
				t.Fatalf("refusal JSON = %q (%v), want code proxy.migrate.unsupported", stdout, err)
			}
		})
	}
}
