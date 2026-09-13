package ownershiphandoffv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The committed journal is a two-sided contract. bd writes it; the caller reads
// it — Gas City does, in cmd/gc/dolt_handoff_projection.go — to decide whether a
// scope is still its to manage. Nothing links the two repositories at build
// time, so a renamed field or a moved path on either side would silently become
// "no handoff ever happened", which is the failure mode that made a handoff
// invisible to the caller once already.
//
// The types below are the reader's view, transcribed here on purpose. The point
// is to fail HERE when bd's journal stops satisfying the reader the caller
// actually runs. When the reader changes, change this with it.
//
// Two deliberate differences from the version this replaces, both consequences
// of schema v2:
//
//   - city_root is gone. The journal carries no caller vocabulary; root is the
//     workspace and that is all bd knows.
//   - target identity moved out of the snapshot into its own `target` object.
//     The snapshot is what bd must put BACK; the target is what bd made. Mixing
//     them made the snapshot's meaning ambiguous. The caller's reader has to
//     move with this — it is a coordinated change, not a compatible one.
type projectionEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type projectionJournal struct {
	SchemaVersion int `json:"schema_version"`
	Request       struct {
		Root      string             `json:"root"`
		Database  string             `json:"database"`
		Workspace string             `json:"workspace"`
		Endpoint  projectionEndpoint `json:"endpoint"`
		Owner     string             `json:"owner"`
	} `json:"request"`
	Target struct {
		PID          int    `json:"pid"`
		Birth        string `json:"birth"`
		DataDir      string `json:"data_dir"`
		LaunchID     string `json:"launch_id"`
		LaunchConfig string `json:"launch_config"`
		Executable   string `json:"executable"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
	} `json:"target"`
	Phase     string `json:"phase"`
	Owner     string `json:"owner"`
	UpdatedAt string `json:"updated_at"`
}

// A committed journal must decode into the reader's view with every field the
// reader needs populated — and must be found at the path the reader looks in.
func TestCommittedJournalSatisfiesTheCallersProjection(t *testing.T) {
	root := canonicalTempDir(t)
	beadsDir := BeadsDir(root)
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	j := Journal{
		Phase: PhaseCommitted,
		Owner: OwnerBD,
		Request: Request{
			Root:      root,
			Database:  "scope_db",
			Workspace: "workspace-uuid",
			Endpoint:  Endpoint{Host: "127.0.0.1", Port: 3307},
			Owner:     OwnerLegacy,
		},
		Target: Target{
			PID:          4242,
			Birth:        "linux-v1:12345",
			DataDir:      filepath.Join(beadsDir, "dolt"),
			LaunchID:     "0123456789abcdef0123456789abcdef",
			LaunchConfig: filepath.Join(beadsDir, "dolt-handoff-0123456789abcdef0123456789abcdef.yaml"),
			Executable:   "/usr/local/bin/dolt",
			Host:         "127.0.0.1",
			Port:         3399,
		},
		Reservations: Reservations{TargetLaunch: ReservationDone, CommitWriteSet: ReservationDone},
	}
	// The reader looks here, and only here.
	path := JournalPath(root)
	if err := save(path, j); err != nil {
		t.Fatalf("save: %v", err)
	}
	if want := filepath.Join(root, ".beads", "ownership-handoff.json"); path != want {
		t.Fatalf("journal path is %s, want %s", path, want)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var seen projectionJournal
	if err := json.Unmarshal(raw, &seen); err != nil {
		t.Fatalf("the caller's reader cannot decode this journal: %v", err)
	}

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"schema_version", seen.SchemaVersion, SchemaVersion},
		{"phase", seen.Phase, string(PhaseCommitted)},
		{"owner", seen.Owner, string(OwnerBD)},
		{"request.root", seen.Request.Root, root},
		{"request.database", seen.Request.Database, "scope_db"},
		{"request.workspace", seen.Request.Workspace, "workspace-uuid"},
		{"request.endpoint.host", seen.Request.Endpoint.Host, "127.0.0.1"},
		{"request.endpoint.port", seen.Request.Endpoint.Port, 3307},
		{"request.owner", seen.Request.Owner, string(OwnerLegacy)},
		{"target.pid", seen.Target.PID, 4242},
		{"target.birth", seen.Target.Birth, "linux-v1:12345"},
		{"target.data_dir", seen.Target.DataDir, j.Target.DataDir},
		{"target.launch_id", seen.Target.LaunchID, j.Target.LaunchID},
		{"target.launch_config", seen.Target.LaunchConfig, j.Target.LaunchConfig},
		{"target.executable", seen.Target.Executable, "/usr/local/bin/dolt"},
		{"target.host", seen.Target.Host, "127.0.0.1"},
		{"target.port", seen.Target.Port, 3399},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s is %v, the caller's reader expects %v", check.field, check.got, check.want)
		}
	}
	if seen.UpdatedAt == "" {
		t.Error("updated_at is empty; the reader uses it to age a journal")
	}
}

// No caller vocabulary is persisted. city_root in particular is gone: bd knows
// about workspaces, and a field naming the caller's own layout would make bd's
// format depend on a program it must not know exists.
func TestJournalPersistsNoCallerVocabulary(t *testing.T) {
	root := canonicalTempDir(t)
	path := filepath.Join(root, JournalName)
	if err := save(path, journalAt(t, root, PhaseCommitted)); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	request, _ := decoded["request"].(map[string]any)
	for _, forbidden := range []string{"city_root", "city", "gc_root", "scope_root", "provider"} {
		if _, present := decoded[forbidden]; present {
			t.Errorf("journal has a top-level %q field", forbidden)
		}
		if _, present := request[forbidden]; present {
			t.Errorf("journal request has a %q field", forbidden)
		}
	}
	// The request is exactly the merged base shape and nothing more.
	want := map[string]bool{"root": true, "database": true, "workspace": true, "endpoint": true, "owner": true}
	for key := range request {
		if !want[key] {
			t.Errorf("journal request has an unexpected field %q", key)
		}
	}
	for key := range want {
		if _, present := request[key]; !present {
			t.Errorf("journal request is missing %q", key)
		}
	}
}
