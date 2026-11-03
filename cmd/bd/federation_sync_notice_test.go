package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// federationSyncStoreStub serves two peers whose Sync results are given in
// their JSON wire form.
type federationSyncStoreStub struct {
	storage.DoltStorage
	results map[string]string
}

func (s *federationSyncStoreStub) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return []storage.RemoteInfo{{Name: "origin"}, {Name: "alpha"}, {Name: "beta"}}, nil
}

func (s *federationSyncStoreStub) Sync(_ context.Context, peer, _ string) (*storage.SyncResult, error) {
	var res storage.SyncResult
	if err := json.Unmarshal([]byte(s.results[peer]), &res); err != nil {
		return nil, err
	}
	res.Peer = peer
	if !res.Pushed {
		res.PushError = context.DeadlineExceeded
	}
	return &res, nil
}

func runFederationSyncForTest(t *testing.T, stub *federationSyncStoreStub, asJSON bool) (stdout, stderr string) {
	t.Helper()
	savedStore, savedCtx, savedJSON, savedPeer, savedStrategy := store, rootCtx, jsonOutput, federationPeer, federationStrategy
	t.Cleanup(func() {
		store, rootCtx, jsonOutput, federationPeer, federationStrategy = savedStore, savedCtx, savedJSON, savedPeer, savedStrategy
	})
	store, rootCtx, jsonOutput, federationPeer, federationStrategy = stub, context.Background(), asJSON, "", ""
	var err error
	stdout, stderr = captureStdio(t, func() {
		err = runFederationSync(federationSyncCmd, nil)
	})
	if err != nil {
		t.Fatalf("runFederationSync: %v", err)
	}
	return stdout, stderr
}

// One CLI invocation prints the history-visibility notice once when any peer
// publication withheld rows, in human and JSON mode, and never for a failed
// or unfiltered push. JSON output stays valid and marks each filtered result.
func TestFederationSyncFilteredPushNoticeOncePerInvocation(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_JSON_ENVELOPE", "0")
	// Every notice names the setting that caused it.
	const noticeMarker = "federation.exclude_types"
	filtered := `{"Fetched":true,"Merged":true,"Pushed":true,"PushFiltered":true}`
	plain := `{"Fetched":true,"Merged":true,"Pushed":true}`
	failed := `{"Fetched":true,"Merged":true}`

	for _, tc := range []struct {
		name        string
		alpha, beta string
		asJSON      bool
		notices     int
	}{
		{"human both filtered", filtered, filtered, false, 1},
		{"json both filtered", filtered, filtered, true, 1},
		{"json filtered and failed", filtered, failed, true, 1},
		{"human unfiltered and failed", plain, failed, false, 0},
		{"json unfiltered and failed", plain, failed, true, 0},
	} {
		stub := &federationSyncStoreStub{results: map[string]string{"alpha": tc.alpha, "beta": tc.beta}}
		stdout, stderr := runFederationSyncForTest(t, stub, tc.asJSON)
		if got := strings.Count(stderr, noticeMarker); got != tc.notices {
			t.Errorf("%s: %d notices on stderr, want %d:\n%s", tc.name, got, tc.notices, stderr)
		}
		if strings.Contains(stdout, noticeMarker) {
			t.Errorf("%s: notice written to stdout", tc.name)
		}
		if !tc.asJSON {
			continue
		}
		var out struct {
			Results []struct {
				Peer         string
				PushFiltered bool
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("%s: stdout is not valid JSON: %v\n%s", tc.name, err, stdout)
		}
		if len(out.Results) != 2 {
			t.Fatalf("%s: %d results, want 2", tc.name, len(out.Results))
		}
		for i, want := range []string{tc.alpha, tc.beta} {
			if got := out.Results[i].PushFiltered; got != (want == filtered) {
				t.Errorf("%s: %s PushFiltered=%v", tc.name, out.Results[i].Peer, got)
			}
		}
	}
}
