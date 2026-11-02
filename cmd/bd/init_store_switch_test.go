package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadStoreSwitchRecord pins the record reader's contract: an absent
// workspace is not a switch, but a present-but-unreadable or malformed
// metadata file is an error — this guard decides whether a destructive
// operation may proceed, so unlike the count-based helpers it must not
// swallow that case into an empty workspace.
func TestLoadStoreSwitchRecord(t *testing.T) {
	t.Run("absent workspace is not a switch", func(t *testing.T) {
		rec, err := loadStoreSwitchRecord(filepath.Join(t.TempDir(), "nope", ".beads"))
		if err != nil {
			t.Fatalf("missing workspace: %v", err)
		}
		if rec != (storeSwitchRecord{}) {
			t.Errorf("missing workspace record = %+v, want zero", rec)
		}
	})

	t.Run("malformed metadata is an error, never an empty workspace", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("seed malformed metadata: %v", err)
		}
		if _, err := loadStoreSwitchRecord(beadsDir); err == nil {
			t.Fatal("malformed metadata.json must be an error, not an empty workspace")
		}
	})

	t.Run("recorded fields are read raw, mode normalized", func(t *testing.T) {
		beadsDir := t.TempDir()
		body := `{"backend":"dolt","dolt_mode":"  SERVER  ","dolt_database":"la"}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(body), 0o644); err != nil {
			t.Fatalf("seed metadata: %v", err)
		}
		rec, err := loadStoreSwitchRecord(beadsDir)
		if err != nil {
			t.Fatalf("loadStoreSwitchRecord: %v", err)
		}
		want := storeSwitchRecord{Backend: "dolt", Mode: "server", Database: "la"}
		if rec != want {
			t.Errorf("record = %+v, want %+v", rec, want)
		}
	})

	t.Run("unset mode stays distinguishable from deliberately embedded", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
			t.Fatalf("seed metadata: %v", err)
		}
		rec, err := loadStoreSwitchRecord(beadsDir)
		if err != nil {
			t.Fatalf("loadStoreSwitchRecord: %v", err)
		}
		if rec.Mode != "" {
			t.Errorf("absent dolt_mode read back as %q; defaulting here would make every fresh workspace a mode change", rec.Mode)
		}
	})
}

// TestCompareStoreSwitchDatabaseAxis is the comparison half of the guard:
// only a nonempty explicit database differing byte-for-byte from a
// nonempty recorded name is a switch; an absent override means reuse, and
// unknown recorded state fails open to the existing count policy rather
// than inventing a switch.
func TestCompareStoreSwitchDatabaseAxis(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded storeSwitchRecord
		request  storeSwitchRequest
		switched bool
	}{
		{"explicit different database switches", storeSwitchRecord{Backend: "dolt", Mode: "embedded", Database: "fi"}, storeSwitchRequest{Database: "other"}, true},
		{"same database reuses the store", storeSwitchRecord{Backend: "dolt", Mode: "embedded", Database: "fi"}, storeSwitchRequest{Database: "fi"}, false},
		{"absent override means reuse, not the prefix default", storeSwitchRecord{Backend: "dolt", Mode: "embedded", Database: "fi"}, storeSwitchRequest{}, false},
		{"empty recorded name cannot be switched away from", storeSwitchRecord{Backend: "dolt", Mode: "embedded"}, storeSwitchRequest{Database: "other"}, false},
		{"case-only difference is a byte-for-byte difference", storeSwitchRecord{Backend: "dolt", Mode: "embedded", Database: "fi"}, storeSwitchRequest{Database: "Fi"}, true},
		{"non-Dolt backend retains existing validation", storeSwitchRecord{Backend: "postgres", Mode: "embedded", Database: "fi"}, storeSwitchRequest{Database: "other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diff, switched := compareStoreSwitch(tc.recorded, tc.request)
			if switched != tc.switched {
				t.Fatalf("compareStoreSwitch(%+v, %+v) switched = %v, want %v; diff %+v", tc.recorded, tc.request, switched, tc.switched, diff)
			}
			if switched {
				if !diff.DatabaseSwitch {
					t.Error("switch without DatabaseSwitch set")
				}
				if diff.RecordedDatabase != tc.recorded.Database || diff.RequestedDatabase != tc.request.Database {
					t.Errorf("diff names = (%q -> %q), want (%q -> %q)", diff.RecordedDatabase, diff.RequestedDatabase, tc.recorded.Database, tc.request.Database)
				}
			}
		})
	}
}

// TestConfirmStoreSwitchRequiresToken is the non-interactive authorization
// half: a matching token authorizes the switch once, anything else refuses
// with ExitDestroyTokenMissing, and the refusal names both sides without
// ever echoing the token value or a destructive invocation (ADR 0002
// Invariant 4).
func TestConfirmStoreSwitchRequiresToken(t *testing.T) {
	diff := storeSwitchDiff{DatabaseSwitch: true, RecordedDatabase: "fi", RequestedDatabase: "other"}
	run := func(t *testing.T, destroyToken string) (string, int) {
		t.Helper()
		var sb strings.Builder
		err := confirmStoreSwitch(confirmStoreSwitchInput{
			Diff:          diff,
			DestroyToken:  destroyToken,
			ExpectedToken: "DESTROY-fi",
			Stderr:        &sb,
		})
		if err == nil {
			return sb.String(), 0
		}
		var exit *exitError
		if !errors.As(err, &exit) {
			t.Fatalf("confirmStoreSwitch error = %T(%v), want *exitError", err, err)
		}
		return sb.String(), exit.Code
	}

	t.Run("no token refuses with 12", func(t *testing.T) {
		out, code := run(t, "")
		if code != ExitDestroyTokenMissing {
			t.Errorf("missing token exit = %d, want %d", code, ExitDestroyTokenMissing)
		}
		if !strings.Contains(out, "bd help init-safety") {
			t.Errorf("refusal must point at the token-format reference, got:\n%s", out)
		}
	})

	t.Run("wrong token refuses with 12", func(t *testing.T) {
		if _, code := run(t, "DESTROY-wrong"); code != ExitDestroyTokenMissing {
			t.Errorf("wrong token exit = %d, want %d", code, ExitDestroyTokenMissing)
		}
	})

	t.Run("matching token authorizes once", func(t *testing.T) {
		out, code := run(t, "DESTROY-fi")
		if code != 0 {
			t.Errorf("matching token exit = %d, want 0:\n%s", code, out)
		}
		if !strings.Contains(out, "Destroy token accepted") {
			t.Errorf("acceptance must be visible, got:\n%s", out)
		}
	})

	t.Run("notice names both sides and stays silent on the token", func(t *testing.T) {
		out, _ := run(t, "")
		for _, want := range []string{"fi", "other", "--destroy-token"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "DESTROY-fi") {
			t.Errorf("refusal echoed the destroy-token value (ADR 0002 Invariant 4):\n%s", out)
		}
	})
}

// TestReinitPreflightSwitchNeverCounts proves the load-bearing property of
// the GH#3495 fix: a switch is decided from the recorded metadata, so the
// recorded store is never opened or counted — before a refusal, and after
// an accepted confirmation too.
func TestReinitPreflightSwitchNeverCounts(t *testing.T) {
	dbSwitch := storeSwitchRecord{Backend: "dolt", Mode: "embedded", Database: "fi"}
	newInput := func(t *testing.T, token string, calls *int) reinitPreflightInput {
		t.Helper()
		return reinitPreflightInput{
			ReinitLocal:   true,
			BeadsDir:      "unused",
			Request:       storeSwitchRequest{Database: "other"},
			DestroyToken:  token,
			ExpectedToken: "DESTROY-fi",
			Stderr:        &strings.Builder{},
			LoadRecorded: func(string) (storeSwitchRecord, error) {
				return dbSwitch, nil
			},
			CountExisting: func() (int, error) {
				*calls++
				return 7, nil
			},
		}
	}

	t.Run("refused switch never reaches the count", func(t *testing.T) {
		calls := 0
		in := newInput(t, "", &calls)
		err := runInitReinitPreflight(in)
		var exit *exitError
		if !errors.As(err, &exit) || exit.Code != ExitDestroyTokenMissing {
			t.Fatalf("switch without token = %v, want exit %d", err, ExitDestroyTokenMissing)
		}
		if calls != 0 {
			t.Errorf("refused switch opened or counted the recorded store %d time(s); the decision must not depend on it", calls)
		}
	})

	t.Run("accepted switch never reaches the count either", func(t *testing.T) {
		calls := 0
		in := newInput(t, "DESTROY-fi", &calls)
		if err := runInitReinitPreflight(in); err != nil {
			t.Fatalf("authorized switch: %v", err)
		}
		if calls != 0 {
			t.Errorf("authorized switch counted the recorded store %d time(s); no second destruction prompt may follow", calls)
		}
	})

}

// TestReinitPreflightNoSwitchKeepsCountPolicy pins the unchanged half: with
// no detected switch, the existing issue-count confirmation applies — zero
// issues proceed, a populated store demands prompt or token, and a count
// error proceeds with a warning that says the store could not be
// inspected, never that it was empty.
func TestReinitPreflightNoSwitchKeepsCountPolicy(t *testing.T) {
	sameStore := func(count int, countErr error, calls *int) reinitPreflightInput {
		return reinitPreflightInput{
			ReinitLocal:   true,
			BeadsDir:      "unused",
			Request:       storeSwitchRequest{Database: "fi"},
			DestroyToken:  "DESTROY-fi",
			ExpectedToken: "DESTROY-fi",
			StdinIsTTY:    false,
			Stdin:         strings.NewReader(""),
			Stderr:        &strings.Builder{},
			LoadRecorded: func(string) (storeSwitchRecord, error) {
				return storeSwitchRecord{Backend: "dolt", Mode: "embedded", Database: "fi"}, nil
			},
			CountExisting: func() (int, error) {
				if calls != nil {
					*calls++
				}
				return count, countErr
			},
		}
	}

	t.Run("no reinit short-circuits before any load", func(t *testing.T) {
		calls := 0
		in := sameStore(5, nil, &calls)
		in.ReinitLocal = false
		if err := runInitReinitPreflight(in); err != nil {
			t.Fatalf("plain init preflight: %v", err)
		}
		if calls != 0 {
			t.Errorf("plain init preflight counted %d time(s)", calls)
		}
	})

	t.Run("zero issues proceed without a prompt", func(t *testing.T) {
		if err := runInitReinitPreflight(sameStore(0, nil, nil)); err != nil {
			t.Fatalf("zero-issue reinit: %v", err)
		}
	})

	t.Run("populated store without a token refuses with 12", func(t *testing.T) {
		in := sameStore(3, nil, nil)
		in.DestroyToken = ""
		var exit *exitError
		err := runInitReinitPreflight(in)
		if !errors.As(err, &exit) || exit.Code != ExitDestroyTokenMissing {
			t.Fatalf("populated store without token = %v, want exit 12", err)
		}
	})

	t.Run("count error proceeds with a warning that does not call the store empty", func(t *testing.T) {
		in := sameStore(0, errors.New("server did not answer"), nil)
		stderr := in.Stderr.(*strings.Builder)
		if err := runInitReinitPreflight(in); err != nil {
			t.Fatalf("count error must proceed under the existing policy: %v", err)
		}
		out := stderr.String()
		for _, want := range []string{"could not inspect", "server did not answer"} {
			if !strings.Contains(out, want) {
				t.Errorf("warning missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(strings.ToLower(out), "empty") {
			t.Errorf("warning described the store as empty; an uninspectable store is not an empty one:\n%s", out)
		}
	})

	t.Run("malformed recorded metadata is an error before anything is touched", func(t *testing.T) {
		calls := 0
		in := sameStore(0, nil, &calls)
		in.LoadRecorded = func(string) (storeSwitchRecord, error) {
			return storeSwitchRecord{}, errors.New("parsing config: unexpected token")
		}
		if err := runInitReinitPreflight(in); err == nil {
			t.Fatal("malformed metadata must be an error, not an empty workspace that fails open")
		}
		if calls != 0 {
			t.Errorf("malformed metadata reached the recorded store %d time(s)", calls)
		}
	})
}
