package dolt

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
)

// TestServerEnvNoteLeavesOtherFailuresUnchanged pins that the
// server-environment note (bd-f68dud, GH#5953) changes nothing it has no
// business in: a peer fetch that fails in embedded mode, without matching
// variables, for a scheme with no credential family, or whose destination bd
// cannot look up returns exactly the error it returns with no cloud variables
// set. Where the note is added it is appended to the unchanged message, and
// errors.Is / errors.As still reach the underlying failure.
func TestServerEnvNoteLeavesOtherFailuresUnchanged(t *testing.T) {
	const s3URL = "s3://bucket/beads"
	fetch := func(t *testing.T, url string, o envNoteOpts, env map[string]string) error {
		t.Helper()
		clearPredicateEnv(t)
		clearCloudAuthEnv(t)
		for k, v := range env {
			t.Setenv(k, v)
		}
		store, _ := envNoteStore(t, "peer", url, o)
		err := store.Fetch(context.Background(), "peer")
		if err == nil || !strings.Contains(err.Error(), "failed to fetch from peer peer") {
			t.Fatalf("Fetch = %v, want the SQL route's CALL DOLT_FETCH failure", err)
		}
		return err
	}
	aws := map[string]string{"AWS_ACCESS_KEY_ID": "sentinel-key"}

	for _, tc := range []struct {
		name string
		url  string
		o    envNoteOpts
		env  map[string]string
	}{
		{name: "embedded mode", url: s3URL, o: envNoteOpts{embedded: true}, env: aws},
		{name: "no relevant credentials", url: s3URL, env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/sentinel"}},
		{name: "scheme with no credential family", url: "file:///tmp/remote", env: aws},
		{name: "git protocol on the SQL route", url: "git+https://example.com/org/repo.git", env: aws},
		{name: "destination unavailable", url: s3URL, o: envNoteOpts{remotesErr: true}, env: aws},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := fetch(t, tc.url, tc.o, nil).Error()
			got := fetch(t, tc.url, tc.o, tc.env).Error()
			if got != want {
				t.Fatalf("Fetch error = %q, want it unchanged: %q", got, want)
			}
		})
	}

	t.Run("noted error keeps its identity", func(t *testing.T) {
		bare := fetch(t, s3URL, envNoteOpts{}, nil).Error()
		err := fetch(t, s3URL, envNoteOpts{}, aws)
		checkServerEnvNote(t, err, "DOLT_FETCH", "AWS_*", s3URL)
		if !strings.HasPrefix(err.Error(), bare+"; ") {
			t.Fatalf("Fetch error = %q, want the unnoted error %q with the note appended", err, bare)
		}
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("errors.As(%v, *net.OpError) = false, want the dial failure reachable through the note", err)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("errors.Is(%v, ECONNREFUSED) = false, want the dial failure reachable through the note", err)
		}
	})
}
