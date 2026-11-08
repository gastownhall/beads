package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// ignoredTablesPullErr is a pull the storage layer refused over ignored
// working tables (GH#6368), wrapped the way the store's pull returns it.
func ignoredTablesPullErr() error {
	refusal := errors.New("failed to pull from origin/main: Error 1105 (HY000): error: local changes would be stomped by merge:\n\tevents\n\twisps\n Please commit your changes before you merge.")
	return fmt.Errorf("pull origin: %w", &versioncontrolops.IgnoredTablesMergeError{Tables: []string{"events", "wisps"}, Err: refusal})
}

// TestSyncIgnoredTableStuck pins bd sync's handling of a pull refused over
// ignored tables the remote still tracks: retrying the same state never
// clears it, so the run stops on that attempt without pushing and reports the
// operator-only dirty-stuck exit, naming the tables.
func TestSyncIgnoredTableStuck(t *testing.T) {
	t.Run("loop stops on the attempt without pushing", func(t *testing.T) {
		r := &syncOpsRecorder{
			pullErrs: []error{versioncontrolops.ErrPullBehindFastForwardable, ignoredTablesPullErr()},
		}
		out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
		if err != nil {
			t.Fatalf("runSyncLoop error = %v; want the dirty-stuck outcome", err)
		}
		if out.Status != syncStatusDirtyStuck {
			t.Fatalf("status = %q, want %q", out.Status, syncStatusDirtyStuck)
		}
		if out.Attempts != 2 || r.pulls != 2 || r.pushes != 0 || r.recomputes != 0 {
			t.Fatalf("attempts=%d pulls=%d recomputes=%d pushes=%d; want the run to stop on attempt 2 without recomputing or pushing",
				out.Attempts, r.pulls, r.recomputes, r.pushes)
		}
		if !reflect.DeepEqual(out.IgnoredTablesOnRemote, []string{"events", "wisps"}) {
			t.Fatalf("IgnoredTablesOnRemote = %v, want [events wisps]", out.IgnoredTablesOnRemote)
		}
		if !out.sawTransient(syncTransientPullBehind) {
			t.Fatalf("earlier transient lost: %+v", out.Transients)
		}
	})

	t.Run("a live merge conflict keeps precedence", func(t *testing.T) {
		r := &syncOpsRecorder{
			pullErrs:     []error{ignoredTablesPullErr()},
			conflictsSeq: [][]string{nil, {"issues"}},
		}
		out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
		if err != nil || out.Status != syncStatusConflict {
			t.Fatalf("status = %q, err = %v; want the conflict", out.Status, err)
		}
		if len(out.IgnoredTablesOnRemote) != 0 {
			t.Fatalf("conflict outcome also reports ignored tables: %v", out.IgnoredTablesOnRemote)
		}
	})

	t.Run("an untyped stomp refusal stays a generic error", func(t *testing.T) {
		r := &syncOpsRecorder{
			pullErrs: []error{errors.New("error: local changes would be stomped by merge:\n\tevents\n Please commit your changes before you merge.")},
		}
		if _, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts); err == nil {
			t.Fatal("untyped refusal was not reported as an error")
		}
	})

	for _, noPush := range []bool{false, true} {
		t.Run(fmt.Sprintf("command exits 4 (no-push=%v)", noPush), func(t *testing.T) {
			fake := &fakeSyncStore{pullErr: ignoredTablesPullErr()}
			setupSyncCommandTest(t, fake)
			if noPush {
				t.Setenv("BD_NO_PUSH", "true")
				if err := config.Initialize(); err != nil {
					t.Fatalf("config.Initialize: %v", err)
				}
			}
			jsonOutput = true

			var runErr error
			stdout := captureStdout(t, func() error { runErr = runSyncCommand(syncCmd, nil); return nil })
			if code, ok := exitCodeFromError(runErr); !ok || code != ExitSyncDirtyStuck {
				t.Fatalf("exit = %d (ok=%v, err=%v), want %d", code, ok, runErr, ExitSyncDirtyStuck)
			}
			if fake.pullCalls != 1 || fake.pushCalls != 0 {
				t.Fatalf("pulls=%d pushes=%d, want one pull and no push", fake.pullCalls, fake.pushCalls)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("json.Unmarshal(%q): %v", stdout, err)
			}
			if got["status"] != syncStatusDirtyStuck || got["pushed"] != false {
				t.Fatalf("status/pushed = %v/%v, want %s/false", got["status"], got["pushed"], syncStatusDirtyStuck)
			}
			if !reflect.DeepEqual(got["ignored_tables_on_remote"], []any{"events", "wisps"}) {
				t.Fatalf("ignored_tables_on_remote = %v, want [events wisps]", got["ignored_tables_on_remote"])
			}
		})
	}

	t.Run("human report", func(t *testing.T) {
		msg := strings.Join(syncStuckMessage(&syncOutcome{
			Status:                syncStatusDirtyStuck,
			Attempts:              1,
			IgnoredTablesOnRemote: []string{"events", "wisps"},
			IgnoredTablesError:    ignoredTablesPullErr().Error(),
		}), "\n")
		for _, want := range []string{
			"events, wisps",
			"stomped by merge",
			"bd dolt push",
			"docs/recovery/sync-failures.md#merge-blocked-on-the-events-table-migration-0062",
			"Nothing was pushed",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("report missing %q:\n%s", want, msg)
			}
		}
		for _, banned := range []string{"retry on the next tick", "consecutive sync run(s)", "constraint violation", "--force"} {
			if strings.Contains(msg, banned) {
				t.Errorf("report says %q:\n%s", banned, msg)
			}
		}
		if !strings.Contains(msg, "Do not force-push") {
			t.Errorf("report does not warn off the destructive remedies:\n%s", msg)
		}
	})
}
