package dolt

import (
	"strings"
	"testing"
)

// TestSQLTransferErrorsRedactRemoteCredentials pins the server-mode SQL route,
// the one a store takes when its CLI directory is not proven to hold its
// server's database (an external server, a gateway, a TLS endpoint, a socket).
// A failed CALL DOLT_PUSH to a git+https remote it cannot reach names the
// stored URL, token and all, in Dolt's own error, which reaches the terminal,
// the auto-push warning and the span. Every push route must return it with
// the userinfo dropped and the host and path kept. The pull and fetch routes
// must not leak it either, though Dolt's DOLT_PULL and DOLT_FETCH report only
// git's anonymized line for this failure today.
func TestSQLTransferErrorsRedactRemoteCredentials(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const token = "SYNTHETIC_TOKEN_42"
	if err := store.AddRemote(ctx, "origin", "git+https://x-access-token:"+token+"@127.0.0.1:1/org/repo"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	for name, route := range map[string]func() (bool, error){
		"remote": func() (bool, error) { return store.prepareCLIRouteForGitProtocol(ctx, "origin") },
		"peer":   func() (bool, error) { return store.prepareCLIRouteForPeerGitProtocol(ctx, "origin") },
	} {
		if useCLI, err := route(); err != nil || useCLI {
			t.Fatalf("precondition: %s route = (CLI %v, %v), want the SQL route", name, useCLI, err)
		}
	}

	const named = "'git+https://127.0.0.1:1/org/repo' could not be accessed"
	for _, tc := range []struct {
		name     string
		transfer func() error
		// names reports whether Dolt names the remote's URL in this route's
		// error, so the test can tell the redaction ran.
		names bool
	}{
		{"push", func() error { return store.PushRemote(ctx, "origin", false) }, true},
		{"force push", func() error { return store.PushRemote(ctx, "origin", true) }, true},
		{"peer push", func() error { return store.PushTo(ctx, "origin") }, true},
		{"pull", func() error { return store.PullRemote(ctx, "origin") }, false},
		{"peer pull", func() error { _, err := store.PullFrom(ctx, "origin"); return err }, false},
		{"peer fetch", func() error { return store.Fetch(ctx, "origin") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.transfer()
			if err == nil {
				t.Fatal("transfer to an unreachable remote succeeded")
			}
			msg := err.Error()
			for _, secret := range []string{token, "x-access-token"} {
				if strings.Contains(msg, secret) {
					t.Errorf("error leaks %q: %s", secret, msg)
				}
			}
			if tc.names && !strings.Contains(msg, named) {
				t.Errorf("error does not name the remote as %q: %s", named, msg)
			}
		})
	}
}
