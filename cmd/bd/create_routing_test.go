//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gastownhall/beads/internal/config"
)

func TestGetRoutingConfigValue_DBFallback(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode in DB: %v", err)
	}

	got := getRoutingConfigValue(ctx, s, "routing.mode")
	if got != "auto" {
		t.Fatalf("getRoutingConfigValue() = %q, want %q", got, "auto")
	}
}

func TestGetRoutingConfigValue_YAMLPrecedence(t *testing.T) {
	initConfigForTest(t)

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("failed to set routing.mode in DB: %v", err)
	}
	config.Set("routing.mode", "maintainer")

	got := getRoutingConfigValue(ctx, s, "routing.mode")
	if got != "maintainer" {
		t.Fatalf("getRoutingConfigValue() = %q, want %q", got, "maintainer")
	}
}
