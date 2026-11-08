package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestDefaultRemoteTransferFailureNamesServerEnvironment pins bd-f68dud
// (GH#5953) for the default remote. A server-mode push or pull runs in the
// Dolt server's process whether bd takes the SQL route or the CLI route (the
// dolt CLI hands it to the server serving its directory), so a failed
// transfer gets one note naming the variables bd holds that the server reads
// from its own environment instead, and the remedy. The error stays usable:
// its context is kept and the CLI's exit status is still reachable. A
// failure before the transfer (fsck, or a SQL connection that never reached
// the server) gets no note, and neither does a CLI pull that transferred and
// stopped on conflicts the resolver left for the operator.
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
		expect        func(sqlmock.Sqlmock)
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
			refuse: []string{"pull"},
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
			},
			run:         func(ctx context.Context, s *DoltStore) error { return s.Pull(ctx) },
			wantContext: "dolt pull failed", procedure: "DOLT_PULL", vars: "AWS_*",
			wantExitError: true, wantCLICallIn: "pull origin main",
		},
		{
			name: "CLI cloud-auth pull that stops on operator conflicts", url: s3URL, env: aws, o: envNoteOpts{cli: true},
			refuse:        []string{"pull"},
			expect:        expectDeclinedCLIPullConflicts,
			run:           func(ctx context.Context, s *DoltStore) error { return s.Pull(ctx) },
			wantContext:   "dolt pull failed",
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
			store, mock := envNoteStore(t, "origin", tc.url, tc.o)
			if tc.expect != nil {
				tc.expect(mock)
			}

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

// expectDeclinedCLIPullConflicts serves a writable store's pull whose CLI
// transfer left a conflict on roadmaps, a table the resolver never resolves:
// nothing to commit before the pull, the resolver's session finds the
// conflict and rolls back, and the conflict is still there for the operator.
func expectDeclinedCLIPullConflicts(mock sqlmock.Sqlmock) {
	roadmaps := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("roadmaps", 3)
	}
	mock.ExpectQuery("FROM dolt_status").WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
	mock.ExpectExec("SET @@dolt_allow_commit_conflicts = 1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(roadmaps()) // resolver pre-screen declines roadmaps
	mock.ExpectQuery("FROM dolt_constraint_violations").WillReturnRows(sqlmock.NewRows([]string{"table"}))
	mock.ExpectRollback()
	mock.ExpectExec("SET @@dolt_allow_commit_conflicts = 0").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(roadmaps()) // still there after the rollback
}
