package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/ownershiphandoff"
)

// TestRestoredControlsAreProvenAtTheRestoreAndNotAfterTheRestart states the two
// lifetimes the rollback needs from the same three files.
//
// Byte-exactness decides that the restore happened, and it is checked where it
// is still true: immediately after the files are written, before the restart
// the rollback performs next. After that restart, config.yaml belongs to the
// legacy owner, which canonicalises the config of a city it has just taken
// back; demanding those bytes again refuses every rollback that owner
// completes. What still has to hold is the controls that route the scope.
func TestRestoredControlsAreProvenAtTheRestoreAndNotAfterTheRestart(t *testing.T) {
	root := canonicalTempDir(t)
	beadsDir := filepath.Join(root, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"project_id":"workspace"}`)
	config := []byte("dolt.auto-start: false\ndolt.host: 127.0.0.1\ndolt.port: 3307\ntypes.custom: bug,chore\n")
	port := []byte("3307")
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(beadsDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("metadata.json", metadata)
	write("config.yaml", config)
	write("dolt-server.port", port)
	request := ownershiphandoff.Request{CityRoot: root, Root: root, Database: "beads", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	snapshot := ownershiphandoff.Snapshot{
		WorkspaceMetadata: metadata, WorkspaceMetadataPresent: true, WorkspaceMetadataMode: 0o600,
		WorkspaceConfig: config, WorkspaceConfigPresent: true, WorkspaceConfigMode: 0o600,
		WorkspacePort: port, WorkspacePortPresent: true, WorkspacePortMode: 0o600,
	}
	if err := ownershiphandoff.ValidateRestoredWorkspaceArtifacts(beadsDir, snapshot); err != nil {
		t.Fatalf("restore moment = %v, want the captured bytes accepted", err)
	}
	if err := requireRestartedLegacyControls(beadsDir, request, snapshot); err != nil {
		t.Fatalf("restarted controls = %v, want the captured bytes accepted", err)
	}

	// The legacy owner's vocabulary merge on the restart the rollback asked for.
	write("config.yaml", append(append([]byte{}, config...), []byte("types.extra: startup-health-episode\n")...))
	if err := ownershiphandoff.ValidateRestoredWorkspaceArtifacts(beadsDir, snapshot); err == nil {
		t.Fatal("the restore-moment comparison accepted a rewritten config.yaml")
	}
	if err := requireRestartedLegacyControls(beadsDir, request, snapshot); err != nil {
		t.Fatalf("restarted controls = %v, want the legacy owner's own canonicalisation admitted", err)
	}

	for name, drift := range map[string]func(){
		"auto-start flipped back to bd's staged value": func() {
			write("config.yaml", []byte("dolt.auto-start: true\ndolt.host: 127.0.0.1\ndolt.port: 3307\ntypes.custom: bug,chore\n"))
		},
		"scope routed at another endpoint": func() {
			write("config.yaml", []byte("dolt.auto-start: false\ndolt.host: 127.0.0.1\ndolt.port: 3399\ntypes.custom: bug,chore\n"))
		},
		"control dropped entirely": func() {
			write("config.yaml", []byte("types.custom: bug,chore\n"))
		},
		"workspace identity rewritten": func() {
			write("config.yaml", config)
			write("metadata.json", []byte(`{"project_id":"someone-else"}`))
		},
		"published port rewritten": func() {
			write("metadata.json", metadata)
			write("dolt-server.port", []byte("3399"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			drift()
			t.Cleanup(func() {
				write("metadata.json", metadata)
				write("config.yaml", config)
				write("dolt-server.port", port)
			})
			var coded interface{ HandoffErrorCode() string }
			err := requireRestartedLegacyControls(beadsDir, request, snapshot)
			if err == nil || !errors.As(err, &coded) || coded.HandoffErrorCode() != "identity_changed" {
				t.Fatalf("error=%v, want a typed identity_changed refusal", err)
			}
		})
	}
}
