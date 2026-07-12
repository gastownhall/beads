package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestInstallBackendPluginWritesMetadataAndLocalTrust(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	plugin := filepath.Join(t.TempDir(), "bd-backend-test")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	result, err := installBackendPlugin(beadsDir, backendInstallInput{
		Backend: "doltlite",
		Command: plugin,
		Trace:   "/tmp/backend-plugin.jsonl",
	})
	if err != nil {
		t.Fatalf("installBackendPlugin: %v", err)
	}
	if result.Backend != "doltlite" {
		t.Fatalf("result backend = %q, want doltlite", result.Backend)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if cfg.GetBackend() != "doltlite" {
		t.Fatalf("backend = %q, want doltlite", cfg.GetBackend())
	}
	if cfg.Database != "doltlite" {
		t.Fatalf("database = %q, want doltlite", cfg.Database)
	}
	if cfg.BackendPluginCommand != "" {
		t.Fatalf("backend_plugin_command = %q, want empty because executable trust is local-only", cfg.BackendPluginCommand)
	}
	wantArgs := []string{"--trace", "/tmp/backend-plugin.jsonl", "serve"}
	if len(cfg.BackendPluginArgs) != 0 {
		t.Fatalf("backend_plugin_args = %#v, want empty because executable trust is local-only", cfg.BackendPluginArgs)
	}
	if cfg.DoltDatabase != configfile.DefaultDoltDatabase {
		t.Fatalf("dolt_database = %q, want %q", cfg.DoltDatabase, configfile.DefaultDoltDatabase)
	}
	trusted, err := configfile.ResolveBackendPluginConfig(beadsDir, "doltlite")
	if err != nil {
		t.Fatalf("ResolveBackendPluginConfig: %v", err)
	}
	if trusted == nil {
		t.Fatal("ResolveBackendPluginConfig returned nil, want local trust config")
	}
	if trusted.Command != plugin {
		t.Fatalf("trusted command = %q, want %q", trusted.Command, plugin)
	}
	if !reflect.DeepEqual(trusted.Args, wantArgs) {
		t.Fatalf("trusted args = %#v, want %#v", trusted.Args, wantArgs)
	}
	if trusted.Source != result.TrustPath {
		t.Fatalf("trusted source = %q, want %q", trusted.Source, result.TrustPath)
	}
}

func TestInstallBackendPluginPreservesExistingDatabaseFields(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	cfg := &configfile.Config{
		Backend:      "doltlite",
		Database:     "doltlite",
		DoltDatabase: "hq",
		ProjectID:    "project-1",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	plugin := filepath.Join(t.TempDir(), "bd-backend-test")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	_, err := installBackendPlugin(beadsDir, backendInstallInput{
		Backend: "doltlite",
		Command: plugin,
		Args:    []string{"--log-level", "debug", "serve"},
	})
	if err != nil {
		t.Fatalf("installBackendPlugin: %v", err)
	}
	got, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if got.DoltDatabase != "hq" {
		t.Fatalf("dolt_database = %q, want hq", got.DoltDatabase)
	}
	if got.ProjectID != "project-1" {
		t.Fatalf("project_id = %q, want project-1", got.ProjectID)
	}
	wantArgs := []string{"--log-level", "debug", "serve"}
	if len(got.BackendPluginArgs) != 0 {
		t.Fatalf("backend_plugin_args = %#v, want empty because executable trust is local-only", got.BackendPluginArgs)
	}
	trusted, err := configfile.ResolveBackendPluginConfig(beadsDir, "doltlite")
	if err != nil {
		t.Fatalf("ResolveBackendPluginConfig: %v", err)
	}
	if trusted == nil || !reflect.DeepEqual(trusted.Args, wantArgs) {
		t.Fatalf("trusted plugin args = %#v, want %#v", trusted, wantArgs)
	}
}

func TestInstallBackendPluginRejectsNonExecutableCommand(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	plugin := filepath.Join(t.TempDir(), "bd-backend-test")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	if _, err := installBackendPlugin(beadsDir, backendInstallInput{Backend: "doltlite", Command: plugin}); err == nil {
		t.Fatal("expected non-executable plugin command to be rejected")
	}
}
