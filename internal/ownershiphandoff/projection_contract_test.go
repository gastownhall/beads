package ownershiphandoff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The committed handoff journal is a two-sided contract. bd writes it; Gas City
// reads it, in cmd/gc/dolt_handoff_projection.go, to decide whether a scope is
// still its to manage. Nothing links the two repositories at build time, so a
// renamed field or a changed path convention on either side would silently
// become "no handoff ever happened" — the failure mode that made a handoff
// invisible to gc once already.
//
// The types and checks below are transcribed from that file. They are a copy on
// purpose: the point is to fail here when bd's journal stops satisfying the
// reader gc actually runs. When gc's projection changes, change this with it.

type gcProjectionEndpoint struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Socket string `json:"socket"`
}

type gcProjectionIdentity struct {
	CityRoot       string               `json:"city_root"`
	ScopeRoot      string               `json:"scope_root"`
	Database       string               `json:"database"`
	Workspace      string               `json:"workspace"`
	Endpoint       gcProjectionEndpoint `json:"endpoint"`
	DataDir        string               `json:"data_dir"`
	ConfigFile     string               `json:"config_file"`
	PID            int                  `json:"pid"`
	StartIdentity  string               `json:"start_identity"`
	StartTimeTicks int64                `json:"start_time_ticks"`
	PortHolderPID  int                  `json:"port_holder_pid"`
}

type gcProjectionResponse struct {
	SchemaVersion int                  `json:"schema_version"`
	Operation     string               `json:"operation"`
	Result        string               `json:"result"`
	Owner         string               `json:"owner"`
	Mutates       bool                 `json:"mutates"`
	Identity      gcProjectionIdentity `json:"identity"`
	IdentityToken string               `json:"identity_token"`
	ErrorCode     string               `json:"error_code"`
}

type gcProjectionJournal struct {
	Request struct {
		CityRoot  string               `json:"city_root"`
		Root      string               `json:"root"`
		Database  string               `json:"database"`
		Workspace string               `json:"workspace"`
		Endpoint  gcProjectionEndpoint `json:"endpoint"`
		Owner     string               `json:"owner"`
	} `json:"request"`
	Snapshot struct {
		Metadata                 []byte `json:"metadata"`
		TargetPID                int    `json:"target_pid"`
		TargetBirth              string `json:"target_birth"`
		TargetDataDir            string `json:"target_data_dir"`
		TargetLaunchID           string `json:"target_launch_id"`
		TargetLaunchConfig       string `json:"target_launch_config"`
		TargetLaunchExecutable   string `json:"target_launch_executable"`
		WorkspaceMetadata        []byte `json:"workspace_metadata"`
		WorkspaceConfig          []byte `json:"workspace_config"`
		WorkspacePort            []byte `json:"workspace_port"`
		WorkspaceMetadataPresent bool   `json:"workspace_metadata_present"`
		WorkspaceConfigPresent   bool   `json:"workspace_config_present"`
		WorkspacePortPresent     bool   `json:"workspace_port_present"`
		WorkspaceMetadataMode    uint32 `json:"workspace_metadata_mode"`
		WorkspaceConfigMode      uint32 `json:"workspace_config_mode"`
		WorkspacePortMode        uint32 `json:"workspace_port_mode"`
		Sentinel                 string `json:"sentinel"`
	} `json:"snapshot"`
	SnapshotCaptured     bool   `json:"snapshot_captured"`
	CommitHookInProgress bool   `json:"commit_hook_in_progress"`
	CommitHookRan        bool   `json:"commit_hook_ran"`
	MutationOccurred     bool   `json:"mutation_occurred"`
	Phase                string `json:"phase"`
	Owner                string `json:"owner"`
}

// gcProjectionIdentityToken is gc's handoffIdentityToken: the sha256 of the
// identity's canonical JSON. gc recomputes it from the preserved inspect bytes
// and requires it to equal both the response's own token and the journal's
// sentinel, so bd must store the responder's bytes verbatim rather than
// re-marshalling them through its own types.
func gcProjectionIdentityToken(identity gcProjectionIdentity) string {
	b, _ := json.Marshal(identity)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func gcValidateProjectionRequest(scopeRoot string, journal gcProjectionJournal) error {
	r := journal.Request
	if r.Owner != "legacy-gc" || r.Database == "" || r.Workspace == "" || r.Endpoint.Socket != "" ||
		(r.Endpoint.Host != "127.0.0.1" && r.Endpoint.Host != "localhost" && r.Endpoint.Host != "::1") ||
		r.Endpoint.Port < 1 || r.Endpoint.Port > 65535 {
		return errors.New("ownership handoff journal has invalid request identity")
	}
	if filepath.Clean(r.CityRoot) != scopeRoot || filepath.Clean(r.Root) != scopeRoot {
		return errors.New("ownership handoff journal does not bind this scope root")
	}
	return nil
}

func gcValidateCommittedProjection(scopeRoot string, journal gcProjectionJournal) error {
	if journal.Owner != "bd" || !journal.SnapshotCaptured || !journal.MutationOccurred ||
		!journal.CommitHookRan || journal.CommitHookInProgress {
		return errors.New("ownership handoff journal has incomplete committed checkpoint")
	}
	s := journal.Snapshot
	if s.TargetPID <= 0 || strings.TrimSpace(s.TargetBirth) == "" ||
		filepath.Clean(s.TargetDataDir) != filepath.Join(scopeRoot, ".beads", "dolt") {
		return errors.New("ownership handoff journal has invalid direct target identity")
	}
	if len(s.TargetLaunchID) != 32 || strings.Trim(s.TargetLaunchID, "0123456789abcdef") != "" ||
		s.TargetLaunchConfig != filepath.Join(scopeRoot, ".beads", "dolt-handoff-"+s.TargetLaunchID+".yaml") ||
		!filepath.IsAbs(s.TargetLaunchExecutable) || filepath.Clean(s.TargetLaunchExecutable) != s.TargetLaunchExecutable {
		return errors.New("ownership handoff journal has incomplete strict launch identity")
	}
	return gcValidateProjectionSnapshotIdentity(journal)
}

func gcValidateProjectionSnapshotIdentity(journal gcProjectionJournal) error {
	decoder := json.NewDecoder(bytes.NewReader(journal.Snapshot.Metadata))
	decoder.DisallowUnknownFields()
	var response gcProjectionResponse
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("decode legacy protocol snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("ownership handoff legacy protocol snapshot must contain one object")
	}
	if response.SchemaVersion != 1 || response.Operation != "handoff-inspect" || response.Result != "eligible" ||
		response.Owner != "legacy-gc" || response.Mutates || strings.TrimSpace(response.ErrorCode) != "" {
		return errors.New("ownership handoff journal has invalid legacy protocol snapshot")
	}
	r := journal.Request
	i := response.Identity
	if i.CityRoot != r.CityRoot || i.ScopeRoot != r.Root || i.Database != r.Database || i.Workspace != r.Workspace ||
		i.Endpoint != r.Endpoint || i.PID <= 0 || strings.TrimSpace(i.StartIdentity) == "" ||
		i.StartTimeTicks < 0 || i.PortHolderPID != i.PID {
		return errors.New("ownership handoff legacy identity does not match its request")
	}
	for _, path := range []string{i.DataDir, i.ConfigFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("ownership handoff legacy identity has noncanonical path")
		}
		rel, err := filepath.Rel(r.CityRoot, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("ownership handoff legacy identity path is outside its city root")
		}
	}
	if err := validateIdentityToken(response.IdentityToken); err != nil ||
		response.IdentityToken != gcProjectionIdentityToken(response.Identity) ||
		response.IdentityToken != journal.Snapshot.Sentinel {
		return errors.New("ownership handoff legacy protocol token does not match its identity")
	}
	return nil
}

// committedHandoffFixture writes the journal a successful direct-target handoff
// leaves behind, at the path gc reads it from, and returns the scope root and
// journal path.
func committedHandoffFixture(t *testing.T) (string, string) {
	t.Helper()
	root := canonicalTestDir(t)
	beadsDir := filepath.Join(root, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := Request{CityRoot: root, Root: root, Database: "beads", Workspace: "ws-1",
		Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	identity := gcProjectionIdentity{
		CityRoot: root, ScopeRoot: root, Database: request.Database, Workspace: request.Workspace,
		Endpoint:       gcProjectionEndpoint{Host: request.Endpoint.Host, Port: request.Endpoint.Port},
		DataDir:        filepath.Join(beadsDir, "dolt"),
		ConfigFile:     filepath.Join(root, ".gc", "runtime", "packs", "dolt", "dolt.yaml"),
		PID:            4242,
		StartIdentity:  "4242:88:dolt",
		StartTimeTicks: 88,
		PortHolderPID:  4242,
	}
	metadata, err := json.Marshal(gcProjectionResponse{SchemaVersion: 1, Operation: "handoff-inspect",
		Result: "eligible", Owner: "legacy-gc", Identity: identity,
		IdentityToken: gcProjectionIdentityToken(identity)})
	if err != nil {
		t.Fatal(err)
	}
	launchID := "0123456789abcdef0123456789abcdef"
	journal := Journal{
		Request: request,
		Snapshot: Snapshot{
			Metadata:                 metadata,
			Sentinel:                 gcProjectionIdentityToken(identity),
			WorkspaceMetadata:        []byte(`{"dolt_mode":"server"}`),
			WorkspaceMetadataPresent: true,
			WorkspaceMetadataMode:    0o644,
			WorkspaceConfig:          []byte("dolt:\n  auto-start: true\n"),
			WorkspaceConfigPresent:   true,
			WorkspaceConfigMode:      0o644,
			WorkspacePort:            []byte("3307"),
			WorkspacePortPresent:     true,
			WorkspacePortMode:        0o644,
			TargetPID:                5150,
			TargetBirth:              "5150:99",
			TargetDataDir:            filepath.Join(beadsDir, "dolt"),
			TargetLaunchID:           launchID,
			TargetLaunchConfig:       filepath.Join(beadsDir, "dolt-handoff-"+launchID+".yaml"),
			TargetLaunchExecutable:   filepath.Join(root, "bin", "dolt"),
		},
		SnapshotCaptured: true,
		CommitHookRan:    true,
		MutationOccurred: true,
		Phase:            PhaseCommitted,
		Owner:            OwnerBD,
		UpdatedAt:        time.Now().UTC(),
	}
	journalPath := filepath.Join(beadsDir, JournalName)
	if err := save(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	return root, journalPath
}

func readProjection(t *testing.T, journalPath string) gcProjectionJournal {
	t.Helper()
	data, err := os.ReadFile(journalPath) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	var projected gcProjectionJournal
	if err := json.Unmarshal(data, &projected); err != nil {
		t.Fatalf("gc cannot parse the journal bd wrote: %v", err)
	}
	return projected
}

// TestCommittedJournalSatisfiesGasCityProjection reads a committed journal the
// way gc does — from <scope>/.beads/ownership-handoff.json, with gc's own field
// names — and runs gc's admission checks over it. A drift on either side of the
// contract fails here rather than in production, where it reads as "gc still
// owns this scope" and raises a second server over bd's.
func TestCommittedJournalSatisfiesGasCityProjection(t *testing.T) {
	root, journalPath := committedHandoffFixture(t)
	if want := filepath.Join(root, ".beads", "ownership-handoff.json"); journalPath != want {
		t.Fatalf("journal path = %s, want the path gc reads: %s", journalPath, want)
	}
	projected := readProjection(t, journalPath)
	if err := gcValidateProjectionRequest(root, projected); err != nil {
		t.Fatalf("gc rejected the journal's request identity: %v", err)
	}
	if projected.Phase != "committed" || projected.Owner != "bd" {
		t.Fatalf("gc reads phase=%q owner=%q, want committed/bd", projected.Phase, projected.Owner)
	}
	if err := gcValidateCommittedProjection(root, projected); err != nil {
		t.Fatalf("gc rejected the committed journal bd wrote: %v", err)
	}
	// bd's own admission fence and gc's projection answer the same question
	// about the same bytes; they must not disagree.
	journal, err := Load(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommittedNormalOpen(filepath.Join(root, ".beads"), journal); err != nil {
		t.Fatalf("bd rejected a journal gc accepts: %v", err)
	}
	// The sentinel is the responder's own token over the responder's own
	// identity bytes. bd stores the inspect response verbatim for exactly this
	// reason: bd's Endpoint omits empty fields and would hash differently.
	token, err := snapshotIdentityToken(journal.Request, journal.Snapshot)
	if err != nil {
		t.Fatalf("bd cannot re-derive the snapshot token: %v", err)
	}
	if token != projected.Snapshot.Sentinel {
		t.Fatalf("bd token %q != journal sentinel %q", token, projected.Snapshot.Sentinel)
	}
}

// TestCommittedJournalProjectionRejectsDrift proves the projection above can
// fail: each mutation is a field gc reads and bd must keep writing the same
// way. Without this the contract test could pass against an empty struct.
func TestCommittedJournalProjectionRejectsDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift func(*gcProjectionJournal)
	}{
		{"launch config outside .beads", func(j *gcProjectionJournal) {
			j.Snapshot.TargetLaunchConfig = filepath.Join(filepath.Dir(j.Snapshot.TargetDataDir), "..", "dolt-handoff.yaml")
		}},
		{"launch id is not 32 hex", func(j *gcProjectionJournal) { j.Snapshot.TargetLaunchID = "short" }},
		{"target data dir is not .beads/dolt", func(j *gcProjectionJournal) {
			j.Snapshot.TargetDataDir = filepath.Join(j.Request.Root, "dolt")
		}},
		{"relative launch executable", func(j *gcProjectionJournal) { j.Snapshot.TargetLaunchExecutable = "dolt" }},
		{"no target birth", func(j *gcProjectionJournal) { j.Snapshot.TargetBirth = "" }},
		{"commit hook never ran", func(j *gcProjectionJournal) { j.CommitHookRan = false }},
		{"sentinel is not the responder's token", func(j *gcProjectionJournal) { j.Snapshot.Sentinel = "sha256:" + strings.Repeat("0", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, journalPath := committedHandoffFixture(t)
			projected := readProjection(t, journalPath)
			tc.drift(&projected)
			if err := gcValidateCommittedProjection(root, projected); err == nil {
				t.Fatal("gc accepted a drifted committed journal")
			}
		})
	}
}
