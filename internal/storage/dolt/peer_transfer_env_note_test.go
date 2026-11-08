package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestPeerTransferFailureNamesServerEnvironment pins bd-f68dud (GH#5953) for
// federation push and fetch. A server-mode transfer to a cloud peer runs in
// the Dolt server's process on both routes: CALL DOLT_PUSH/DOLT_FETCH on the
// SQL route, and on the CLI route the dolt CLI hands the transfer to the
// server that serves its directory. So when such a transfer fails while bd
// holds the peer scheme's credential variables, the error keeps its context
// and gains one note saying the server reads those from its own environment,
// and how to give it current ones. A failure before the transfer (fsck, or a
// SQL connection that never reached the server) or after it (the merge Sync
// runs once the fetch landed) gets none.
func TestPeerTransferFailureNamesServerEnvironment(t *testing.T) {
	const s3URL = "s3://bucket/beads"
	const gsURL = "gs://bucket/beads"
	const hostedURL = "https://doltremoteapi.dolthub.com/org/repo"
	aws := map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key", "AWS_SECRET_ACCESS_KEY": "sentinel-secret"}
	google := map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/sentinel/key.json"}

	for _, tc := range []struct {
		name string
		url  string
		env  map[string]string
		o    envNoteOpts
		// refuse lists the fake dolt subcommands that fail; nil runs no fake.
		refuse []string
		run    func(context.Context, *DoltStore) error
		// wantContext is the existing error context the note must not replace.
		wantContext   string
		procedure     string
		vars          string
		wantCLICallIn string
		// wantExitErr requires the dolt subprocess failure to stay
		// identifiable through the note.
		wantExitErr bool
	}{
		{
			name: "SQL push", url: s3URL, env: aws,
			run:         func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, "peer") },
			wantContext: "failed to push to peer peer", procedure: "DOLT_PUSH", vars: "AWS_*",
		},
		{
			name: "SQL fetch", url: s3URL, env: aws,
			run:         func(ctx context.Context, s *DoltStore) error { return s.Fetch(ctx, "peer") },
			wantContext: "failed to fetch from peer peer", procedure: "DOLT_FETCH", vars: "AWS_*",
		},
		{
			name: "SQL push the server never took", url: s3URL, env: aws, o: envNoteOpts{unreachable: true},
			run:         func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, "peer") },
			wantContext: "failed to push to peer peer",
		},
		{
			name: "SQL fetch the server never took", url: s3URL, env: aws, o: envNoteOpts{unreachable: true},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Fetch(ctx, "peer") },
			wantContext: "failed to fetch from peer peer",
		},
		{
			name: "Sync's fetch", url: s3URL, env: aws, o: envNoteOpts{readOnly: true},
			run: func(ctx context.Context, s *DoltStore) error {
				_, err := s.Sync(ctx, "peer", "")
				return err
			},
			wantContext: "fetch failed: failed to fetch from peer peer", procedure: "DOLT_FETCH", vars: "AWS_*",
		},
		{
			name: "CLI push with stored credentials", url: gsURL, env: google,
			o:      envNoteOpts{cli: true, peerUser: "peer-user"},
			refuse: []string{"push"},
			run:    func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, "peer") },
			// The user went with --user, but a gs:// peer is not a remotesapi
			// destination, so the server never reads DOLT_REMOTE_PASSWORD for it.
			wantContext: "push to peer peer failed", procedure: "DOLT_PUSH", vars: "GOOGLE_*",
			wantCLICallIn: "push --user peer-user peer main",
		},
		{
			name: "CLI push to a hosted peer with a stored password", url: hostedURL,
			o:      envNoteOpts{cli: true, peerUser: "peer-user", peerPassword: "sentinel-peer-secret"},
			refuse: []string{"push"},
			run:    func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, "peer") },
			// The user went with --user to a remotesapi destination, so the
			// server reads the stored password as DOLT_REMOTE_PASSWORD.
			wantContext: "push to peer peer failed", procedure: "DOLT_PUSH", vars: "DOLT_REMOTE_PASSWORD",
			wantCLICallIn: "push --user peer-user peer main", wantExitErr: true,
		},
		{
			name: "CLI fetch with stored credentials", url: s3URL, env: aws,
			o:           envNoteOpts{cli: true, peerUser: "peer-user"},
			refuse:      []string{"fetch"},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Fetch(ctx, "peer") },
			wantContext: "fetch from peer peer failed", procedure: "DOLT_FETCH", vars: "AWS_*",
			wantCLICallIn: "fetch --user peer-user peer",
		},
		{
			name: "CLI push whose fsck fails", url: s3URL, env: aws,
			o:           envNoteOpts{cli: true, peerUser: "peer-user"},
			refuse:      []string{"fsck"},
			run:         func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, "peer") },
			wantContext: "fsck",
		},
		{
			name: "Sync whose fetch landed", url: s3URL, env: aws,
			o:      envNoteOpts{cli: true, peerUser: "peer-user", readOnly: true},
			refuse: []string{"push", "pull"},
			run: func(ctx context.Context, s *DoltStore) error {
				_, err := s.Sync(ctx, "peer", "")
				return err
			},
			wantContext:   "merge failed",
			wantCLICallIn: "fetch --user peer-user peer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPredicateEnv(t)
			clearCloudAuthEnv(t)
			callLog := refusingDoltOnPath(t, "peer", tc.url, tc.refuse...)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			store, _ := envNoteStore(t, "peer", tc.url, tc.o)

			err := tc.run(context.Background(), store)

			if err == nil || !strings.Contains(err.Error(), tc.wantContext) {
				t.Fatalf("error = %v, want it to keep %q", err, tc.wantContext)
			}
			checkServerEnvNote(t, err, tc.procedure, tc.vars, tc.url)
			var exitErr *exec.ExitError
			if tc.wantExitErr && !errors.As(err, &exitErr) {
				t.Fatalf("error %q no longer unwraps to the dolt subprocess exit error", err)
			}
			if tc.wantCLICallIn != "" {
				raw, _ := os.ReadFile(callLog)
				if !strings.Contains(string(raw), tc.wantCLICallIn) {
					t.Fatalf("dolt calls %q lack %q: the CLI route was not taken", raw, tc.wantCLICallIn)
				}
			}
		})
	}
}
