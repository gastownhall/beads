package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDefaultRemoteTransferFailureNamesServerEnvironment pins bd-f68dud
// (GH#5953) for the default remote. A server-mode push or pull runs in the
// Dolt server's process whether bd takes the SQL route or the CLI route (the
// dolt CLI hands it to the server serving its directory), so a failed
// transfer gets one note naming the variables bd holds that the server reads
// from its own environment instead, and the remedy. The error stays usable:
// its context is kept and the CLI's exit status is still reachable. A
// failure before the transfer (fsck, or a SQL connection that never reached
// the server) gets no note.
func TestDefaultRemoteTransferFailureNamesServerEnvironment(t *testing.T) {
	const s3URL = "s3://bucket/beads"
	const hosted = "https://doltremoteapi.dolthub.com/org/repo"
	aws := map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key", "AWS_SECRET_ACCESS_KEY": "sentinel-secret"}

	for _, tc := range []struct {
		name          string
		url           string
		env           map[string]string
		o             envNoteOpts
		refuse        []string
		run           func(context.Context, *DoltStore) error
		wantContext   string
		procedure     string
		vars          string
		wantExitError bool
		wantCLICallIn string
	}{
		{
			name: "SQL push", url: s3URL, env: aws,
			run:         func(ctx context.Context, s *DoltStore) error { return s.Push(ctx) },
			wantContext: "failed to push to origin/main", procedure: "DOLT_PUSH", vars: "AWS_*",
		},
		{
			name: "SQL force push", url: s3URL, env: aws,
			run:         func(ctx context.Context, s *DoltStore) error { return s.ForcePush(ctx) },
			wantContext: "failed to force push to origin/main", procedure: "DOLT_PUSH", vars: "AWS_*",
		},
		{
			name: "SQL push the server never took", url: s3URL, env: aws, o: envNoteOpts{unreachable: true},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Push(ctx) },
			wantContext: "failed to push to origin/main",
		},
		{
			name: "SQL force push the server never took", url: s3URL, env: aws, o: envNoteOpts{unreachable: true},
			run:         func(ctx context.Context, s *DoltStore) error { return s.ForcePush(ctx) },
			wantContext: "failed to force push to origin/main",
		},
		{
			name: "SQL pull", url: s3URL, env: aws, o: envNoteOpts{readOnly: true},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Pull(ctx) },
			wantContext: "failed to pull from origin/main: Error 1105 (HY000): " + refusedCallMessage,
			procedure:   "DOLT_PULL", vars: "AWS_*",
		},
		{
			name: "SQL pull the server never took", url: s3URL, env: aws, o: envNoteOpts{readOnly: true, unreachable: true},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Pull(ctx) },
			wantContext: "failed to pull from origin/main",
		},
		{
			name: "CLI cloud-auth push", url: s3URL, env: aws, o: envNoteOpts{cli: true},
			refuse:      []string{"push"},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Push(ctx) },
			wantContext: "dolt push failed", procedure: "DOLT_PUSH", vars: "AWS_*",
			wantExitError: true, wantCLICallIn: "push origin main",
		},
		{
			name: "CLI credential push", url: hosted,
			o:           envNoteOpts{cli: true, remoteUser: "u", remotePassword: "sentinel-pw"},
			refuse:      []string{"push"},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Push(ctx) },
			wantContext: "dolt push failed", procedure: "DOLT_PUSH", vars: "DOLT_REMOTE_PASSWORD",
			wantExitError: true, wantCLICallIn: "push --user u origin main",
		},
		{
			name: "CLI push whose fsck fails", url: s3URL, env: aws, o: envNoteOpts{cli: true},
			refuse:      []string{"fsck"},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Push(ctx) },
			wantContext: "fsck",
		},
		{
			name: "CLI cloud-auth pull", url: s3URL, env: aws, o: envNoteOpts{cli: true, readOnly: true},
			refuse:      []string{"pull"},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Pull(ctx) },
			wantContext: "dolt pull failed", procedure: "DOLT_PULL", vars: "AWS_*",
			wantExitError: true, wantCLICallIn: "pull origin main",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPredicateEnv(t)
			clearCloudAuthEnv(t)
			callLog := refusingDoltOnPath(t, "origin", tc.url, tc.refuse...)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			store, _ := envNoteStore(t, "origin", tc.url, tc.o)

			err := tc.run(context.Background(), store)

			if err == nil || !strings.Contains(err.Error(), tc.wantContext) {
				t.Fatalf("error = %v, want it to keep %q", err, tc.wantContext)
			}
			checkServerEnvNote(t, err, tc.procedure, tc.vars, tc.url)
			if tc.wantExitError {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("errors.As(%v, *exec.ExitError) = false, want the CLI's exit status reachable", err)
				}
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
