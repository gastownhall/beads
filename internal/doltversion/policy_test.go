package doltversion

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCheckRequired(t *testing.T) {
	for _, v := range []string{"1.52.1", "2.4.2", "2.5.0", "2.5.1"} {
		err := CheckRequired(MustParse(v), `dolt binary "/x/dolt"`, false)
		if !errors.Is(err, ErrBelowRequiredMin) {
			t.Fatalf("CheckRequired(%s) = %v, want ErrBelowRequiredMin", v, err)
		}
		var below *BelowMinimumError
		if !errors.As(err, &below) {
			t.Fatalf("CheckRequired(%s) = %T, want *BelowMinimumError", v, err)
		}
		if below.Detected.String() != v || below.Required.String() != RequiredMin.String() {
			t.Errorf("CheckRequired(%s) fields = %+v", v, below)
		}
	}
	for _, v := range []string{"2.6.0", "2.6.0-dev", "2.6.1", "2.7.0", "3.0.0"} {
		if err := CheckRequired(MustParse(v), "x", false); err != nil {
			t.Errorf("CheckRequired(%s) = %v, want nil", v, err)
		}
	}
}

func TestRequiredMinIs260(t *testing.T) {
	// Julian's 2026-10 decision: dolt 2.6.0 is the minimum bd runs against
	// outside embedded mode. Lowering it is a policy change, not a refactor.
	if RequiredMin.String() != "2.6.0" {
		t.Errorf("RequiredMin = %s, want 2.6.0", RequiredMin)
	}
}

func TestBelowMinimumErrorMessage(t *testing.T) {
	// The refusal must be actionable on its own: what was checked, the
	// version found, the minimum, where to get dolt, and what changed.
	binary := CheckRequired(MustParse("2.5.1"), `dolt binary "/usr/local/bin/dolt"`, false).Error()
	for _, want := range []string{
		`dolt binary "/usr/local/bin/dolt"`, "2.5.1", "requires dolt 2.6.0 or newer",
		"Upgrade dolt to 2.6.0", installDocsURL, "https://github.com/dolthub/dolt/releases",
		releaseNotesURL,
	} {
		if !strings.Contains(binary, want) {
			t.Errorf("binary error = %q, want it to mention %q", binary, want)
		}
	}
	if strings.Contains(binary, "restart") {
		t.Errorf("binary error = %q, want no restart advice (bd launches the binary itself)", binary)
	}

	server := CheckRequired(MustParse("2.1.8"), "dolt sql-server at 127.0.0.1:3307", true).Error()
	for _, want := range []string{"dolt sql-server at 127.0.0.1:3307", "2.1.8", "restart the server"} {
		if !strings.Contains(server, want) {
			t.Errorf("server error = %q, want it to mention %q", server, want)
		}
	}
}

func TestWarningMessage(t *testing.T) {
	unverifiable := &Warning{kind: warningUnverifiable, RawOutput: "garbage output"}
	msg := unverifiable.Message()
	if !strings.Contains(msg, "garbage output") {
		t.Errorf("Message() = %q, want it to include the raw output", msg)
	}
	if !strings.Contains(msg, RequiredMin.String()) {
		t.Errorf("Message() = %q, want it to name the required minimum", msg)
	}
}

func TestCheckServer(t *testing.T) {
	cases := []struct {
		name         string
		answer       string
		queryErr     error
		wantBelow    bool
		wantUndeterm bool
	}{
		{name: "required", answer: "2.6.0"},
		{name: "newer", answer: "2.7.1"},
		{name: "older", answer: "2.5.1", wantBelow: true},
		{name: "much older", answer: "1.86.1", wantBelow: true},
		{name: "unparseable proceeds", answer: "SET_BY_INIT", wantUndeterm: true},
		{name: "query error proceeds", queryErr: errors.New("Error 1105: function: 'dolt_version' not found"), wantUndeterm: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			q := mock.ExpectQuery(regexp.QuoteMeta("SELECT dolt_version()"))
			if tc.queryErr != nil {
				q.WillReturnError(tc.queryErr)
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"dolt_version()"}).AddRow(tc.answer))
			}
			var undetermined error
			err = CheckServer(context.Background(), db, "dolt sql-server at h:1", func(e error) { undetermined = e })
			if tc.wantBelow {
				var below *BelowMinimumError
				if !errors.As(err, &below) || !below.Server || below.Detected.String() != tc.answer {
					t.Fatalf("CheckServer = %v, want *BelowMinimumError for server %s", err, tc.answer)
				}
			} else if err != nil {
				t.Fatalf("CheckServer = %v, want nil", err)
			}
			if (undetermined != nil) != tc.wantUndeterm {
				t.Errorf("undetermined = %v, want called=%v", undetermined, tc.wantUndeterm)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestProbeWithPolicyDemotesUnparseableVersionToWarning(t *testing.T) {
	dir := t.TempDir()
	stub := writeVersionEchoStub(t, dir, "dolt-garbage", "not a version")

	id, warn, err := ProbeWithPolicy(context.Background(), stub)
	if err != nil {
		t.Fatalf("ProbeWithPolicy on unparseable version: unexpected error %v", err)
	}
	if warn == nil {
		t.Fatal("ProbeWithPolicy on unparseable version: warn = nil, want non-nil")
	}
	if warn.RawOutput != "not a version" {
		t.Errorf("warn.RawOutput = %q, want %q", warn.RawOutput, "not a version")
	}
	if len(id.Version.Segments) != 0 {
		t.Errorf("ProbeWithPolicy on unparseable version: Version = %v, want zero value", id.Version)
	}
	// File identity is still populated even though the version didn't parse.
	if id.RealPath == "" {
		t.Error("ProbeWithPolicy on unparseable version: RealPath is empty, want it populated")
	}
}

func TestProbeWithPolicyStillErrorsOnBrokenBinary(t *testing.T) {
	dir := t.TempDir()

	t.Run("probe failure is still an error", func(t *testing.T) {
		stub := writeExecStub(t, dir, "dolt-fails", "#!/bin/sh\nexit 1\n", "@exit /b 1\r\n")
		_, _, err := ProbeWithPolicy(context.Background(), stub)
		if err == nil {
			t.Fatal("ProbeWithPolicy on failing binary: want error, got nil")
		}
		if !errors.Is(err, ErrProbeFailed) {
			t.Errorf("ProbeWithPolicy error = %v, want wrapping ErrProbeFailed", err)
		}
	})

	t.Run("not-found is still an error", func(t *testing.T) {
		_, _, err := ProbeWithPolicy(context.Background(), dir+"/does-not-exist")
		if err == nil {
			t.Fatal("ProbeWithPolicy on missing binary: want error, got nil")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ProbeWithPolicy error = %v, want wrapping ErrNotFound", err)
		}
	})

	t.Run("timeout is still an error", func(t *testing.T) {
		stub := writeExecStub(t, dir, "dolt-sleeps",
			"#!/bin/sh\nsleep 30\n",
			"@ping -n 31 127.0.0.1 > nul\r\n")
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		_, _, err := ProbeWithPolicy(ctx, stub)
		if err == nil {
			t.Fatal("ProbeWithPolicy on timeout: want error, got nil")
		}
		if !errors.Is(err, ErrProbeFailed) {
			t.Errorf("ProbeWithPolicy error = %v, want wrapping ErrProbeFailed", err)
		}
	})
}

func TestProbeWithPolicyRequiredVersion(t *testing.T) {
	dir := t.TempDir()

	t.Run("required version passes", func(t *testing.T) {
		stub := writeVersionEchoStub(t, dir, "dolt-recent", "dolt version 2.6.0")
		_, warn, err := ProbeWithPolicy(context.Background(), stub)
		if err != nil {
			t.Fatalf("ProbeWithPolicy: %v", err)
		}
		if warn != nil {
			t.Errorf("ProbeWithPolicy warn = %v, want nil", warn)
		}
	})

	t.Run("old version is refused", func(t *testing.T) {
		stub := writeVersionEchoStub(t, dir, "dolt-old", "dolt version 2.5.1")
		id, warn, err := ProbeWithPolicy(context.Background(), stub)
		var below *BelowMinimumError
		if !errors.As(err, &below) {
			t.Fatalf("ProbeWithPolicy err = %v, want *BelowMinimumError", err)
		}
		// The message quotes the path (%q), which doubles Windows backslashes.
		if below.Server || !strings.Contains(err.Error(), fmt.Sprintf("%q", stub)) {
			t.Errorf("ProbeWithPolicy err = %q, want a binary error naming %s", err, stub)
		}
		if warn != nil {
			t.Errorf("ProbeWithPolicy warn = %v, want nil alongside the error", warn)
		}
		if id.Version.String() != "2.5.1" {
			t.Errorf("Identity.Version = %v, want 2.5.1 (identity is returned with the refusal)", id.Version)
		}
	})
}
