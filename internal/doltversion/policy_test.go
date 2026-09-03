package doltversion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCheckRecommended(t *testing.T) {
	old := Identity{Version: MustParse("1.52.1")}
	if w := CheckRecommended(old); w == nil {
		t.Error("CheckRecommended(1.52.1) = nil, want warning (below RecommendedMin)")
	} else if w.Probed.String() != "1.52.1" || w.Recommended.String() != RecommendedMin.String() {
		t.Errorf("CheckRecommended warning fields = %+v", w)
	}

	newEnough := Identity{Version: MustParse("2.0.0")}
	if w := CheckRecommended(newEnough); w != nil {
		t.Errorf("CheckRecommended(2.0.0) = %v, want nil", w)
	}

	newer := Identity{Version: MustParse("2.1.0")}
	if w := CheckRecommended(newer); w != nil {
		t.Errorf("CheckRecommended(2.1.0) = %v, want nil", w)
	}
}

func TestWarningMessage(t *testing.T) {
	old := &Warning{kind: warningOldVersion, Probed: MustParse("1.52.1"), Recommended: RecommendedMin}
	if msg := old.Message(); msg == "" {
		t.Error("Message() for old-version warning is empty")
	}

	unverifiable := &Warning{kind: warningUnverifiable, RawOutput: "garbage output"}
	msg := unverifiable.Message()
	if msg == "" {
		t.Error("Message() for unverifiable warning is empty")
	}
	if !strings.Contains(msg, "garbage output") {
		t.Errorf("Message() = %q, want it to include the raw output", msg)
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

func TestProbeWithPolicyRecommendedVersion(t *testing.T) {
	dir := t.TempDir()

	t.Run("recommended version produces no warning", func(t *testing.T) {
		stub := writeVersionEchoStub(t, dir, "dolt-recent", "dolt version 2.0.0")
		_, warn, err := ProbeWithPolicy(context.Background(), stub)
		if err != nil {
			t.Fatalf("ProbeWithPolicy: %v", err)
		}
		if warn != nil {
			t.Errorf("ProbeWithPolicy warn = %v, want nil", warn)
		}
	})

	t.Run("old version produces warning not error", func(t *testing.T) {
		stub := writeVersionEchoStub(t, dir, "dolt-old", "dolt version 1.52.1")
		_, warn, err := ProbeWithPolicy(context.Background(), stub)
		if err != nil {
			t.Fatalf("ProbeWithPolicy: %v", err)
		}
		if warn == nil {
			t.Error("ProbeWithPolicy warn = nil, want non-nil for version below RecommendedMin")
		}
	})
}
