package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsRemoteHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "empty string is local", host: "", want: false},
		{name: "127.0.0.1 is local", host: "127.0.0.1", want: false},
		{name: "localhost is local", host: "localhost", want: false},
		{name: "::1 is local", host: "::1", want: false},
		{name: "[::1] is local", host: "[::1]", want: false},
		{name: "LAN IP is remote", host: "10.0.0.2", want: true},
		{name: "192.168 IP is remote", host: "192.168.1.100", want: true},
		{name: "hostname is remote", host: "mini2", want: true},
		{name: "FQDN is remote", host: "db.example.com", want: true},
		{name: "public IP is remote", host: "203.0.113.1", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isRemoteHost(tt.host)
			if got != tt.want {
				t.Errorf("isRemoteHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestIsRemoteDoltServer_WithRemoteConfig(t *testing.T) {
	// Create a temp .beads dir with a metadata.json pointing to a remote host.
	beadsDir := t.TempDir()
	metadataPath := filepath.Join(beadsDir, "metadata.json")

	metadata := map[string]interface{}{
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "10.0.0.2",
		"dolt_server_port": 3307,
		"database":         "beads",
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	// isRemoteDoltServerForDir lets us test with a specific beads dir
	// rather than relying on FindBeadsDir() which searches the filesystem.
	got := isRemoteDoltServerForDir(beadsDir)
	if !got {
		t.Errorf("isRemoteDoltServerForDir(%q) = false, want true (host=10.0.0.2)", beadsDir)
	}
}

func TestIsRemoteDoltServer_WithLocalConfig(t *testing.T) {
	beadsDir := t.TempDir()
	metadataPath := filepath.Join(beadsDir, "metadata.json")

	metadata := map[string]interface{}{
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3307,
		"database":         "beads",
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	got := isRemoteDoltServerForDir(beadsDir)
	if got {
		t.Errorf("isRemoteDoltServerForDir(%q) = true, want false (host=127.0.0.1)", beadsDir)
	}
}

func TestIsRemoteDoltServer_NoMetadata(t *testing.T) {
	beadsDir := t.TempDir()
	// No metadata.json — should default to local (false).
	got := isRemoteDoltServerForDir(beadsDir)
	if got {
		t.Errorf("isRemoteDoltServerForDir(%q) = true, want false (no metadata)", beadsDir)
	}
}

func TestIsRemoteDoltServer_EmptyHost(t *testing.T) {
	beadsDir := t.TempDir()
	metadataPath := filepath.Join(beadsDir, "metadata.json")

	metadata := map[string]interface{}{
		"backend":  "dolt",
		"database": "beads",
		// No dolt_server_host — defaults to empty string = local
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	got := isRemoteDoltServerForDir(beadsDir)
	if got {
		t.Errorf("isRemoteDoltServerForDir(%q) = true, want false (empty host)", beadsDir)
	}
}
