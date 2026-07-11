package beads

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestOpenConfiguredSelectsSQLite(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{
		Backend:    configfile.BackendSQLite,
		Database:   "beads.db",
		SQLitePath: "configured.sqlite",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	store, info, err := OpenConfigured(t.Context(), beadsDir, OpenConfiguredOptions{})
	if err != nil {
		t.Fatalf("OpenConfigured: %v", err)
	}
	defer store.Close()
	if info.Name != configfile.BackendSQLite {
		t.Fatalf("backend name = %q, want sqlite", info.Name)
	}
	if info.External {
		t.Fatal("SQLite descriptor reports external provider")
	}
	if !info.Capabilities.Embedded || !info.Capabilities.Transactions {
		t.Fatalf("SQLite capabilities = %+v, want embedded transactions", info.Capabilities)
	}
}

func TestOpenFromConfigUsesConfiguredBackend(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{Backend: configfile.BackendSQLite, SQLitePath: "compat.sqlite"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	store, err := OpenFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig: %v", err)
	}
	defer store.Close()
}

func TestOpenConfiguredUnknownBackendFailsClosed(t *testing.T) {
	beadsDir := t.TempDir()
	if err := (&configfile.Config{Backend: "mystery"}).Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	_, _, err := OpenConfigured(t.Context(), beadsDir, OpenConfiguredOptions{})
	if err == nil {
		t.Fatal("OpenConfigured succeeded for unknown backend")
	}
	if !strings.Contains(err.Error(), `backend "mystery"`) || !strings.Contains(err.Error(), "no trusted local command") {
		t.Fatalf("error = %v, want fail-closed unknown backend diagnostic", err)
	}
}
