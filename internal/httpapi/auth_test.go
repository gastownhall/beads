package httpapi

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests are pure: a token file in t.TempDir and an injected clock. The
// rotation property they pin — a token added to the file is accepted without a
// restart, and one removed from it stops being accepted — is the whole reason
// the accepted set is not a startup snapshot.

// fakeClock is the injected time source. Verify's reload gate is the one place
// this package reads a wall clock, so a test that wants to cross the gate moves
// this rather than sleeping a real second.
type fakeClock struct{ nanos atomic.Int64 }

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *fakeClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

// writeTokenFile writes content and returns the path. Every rewrite moves the
// modification time forward explicitly: a test that rewrites a file twice
// inside one filesystem timestamp tick would otherwise be asserting against
// the stat cache rather than against the reload.
func writeTokenFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	touch(t, path, time.Now())
}

func touch(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func newAuthForTest(t *testing.T, content string) (*TokenFileAuth, string, *fakeClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokenFile(t, path, content)
	auth, err := NewTokenFileAuth(path)
	if err != nil {
		t.Fatalf("NewTokenFileAuth: %v", err)
	}
	clock := &fakeClock{}
	clock.nanos.Store(int64(time.Hour))
	auth.now = clock.now
	auth.reloadGate = time.Second
	return auth, path, clock
}

func mustVerify(t *testing.T, a *TokenFileAuth, token string) bool {
	t.Helper()
	ok, err := a.Verify(token)
	if err != nil {
		t.Fatalf("Verify(%q): unexpected reload error: %v", token, err)
	}
	return ok
}

// TestTokenFileAcceptsEveryLine is the rotation mechanism stated as a parse
// rule: every non-empty line is a token and all of them are accepted, which is
// what lets an operator write {new,old}, roll clients, and drop old.
func TestTokenFileAcceptsEveryLine(t *testing.T) {
	auth, _, _ := newAuthForTest(t, "alpha\r\n  bravo  \n\n\ncharlie\n")

	for _, tok := range []string{"alpha", "bravo", "charlie"} {
		if !mustVerify(t, auth, tok) {
			t.Errorf("token %q from the file was refused", tok)
		}
	}
	for _, tok := range []string{"", "  ", "delta", "alpha ", "ALPHA"} {
		if mustVerify(t, auth, tok) {
			t.Errorf("token %q is not in the file but was accepted", tok)
		}
	}
}

// TestNewTokenFileAuthRefusesAnUnusableFile: a server whose token file cannot
// be read must not start. Failing open serves the whole surface
// unauthenticated; failing silently closed answers 401 to every client with
// nothing on stderr saying why.
func TestNewTokenFileAuthRefusesAnUnusableFile(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty")
	writeTokenFile(t, empty, "")
	blank := filepath.Join(dir, "blank")
	writeTokenFile(t, blank, "\n  \n\r\n")
	fat := filepath.Join(dir, "fat")
	writeTokenFile(t, fat, strings.Repeat("a", maxTokenFileBytes+1))

	for _, tc := range []struct{ name, path, want string }{
		{"missing", filepath.Join(dir, "nope"), "no such file"},
		{"empty", empty, "no tokens"},
		{"blank lines only", blank, "no tokens"},
		{"oversized", fat, "larger than"},
		{"unnamed", "", "no token file"},
		{"directory", dir, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTokenFileAuth(tc.path)
			if err == nil {
				t.Fatalf("NewTokenFileAuth(%q) = nil error, want a startup refusal", tc.path)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the refusal (want %q in it)", err, tc.want)
			}
			if !strings.Contains(err.Error(), "token") && !strings.Contains(err.Error(), tc.path) {
				t.Errorf("error %q names neither the token file nor its path", err)
			}
		})
	}
}

// TestTokenRotatesWithoutARestart is the CRITICAL property. A snapshot taken at
// startup would make rotation a pod restart and would make a leaked token
// unrevokable for the life of the process.
func TestTokenRotatesWithoutARestart(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "old-token\n")

	if !mustVerify(t, auth, "old-token") {
		t.Fatal("the token the server started with was refused")
	}

	// Overlap window: both tokens are live, so clients roll one at a time.
	writeTokenFile(t, path, "new-token-value\nold-token\n")
	clock.advance(2 * time.Second)
	if !mustVerify(t, auth, "new-token-value") {
		t.Error("a token added to the file was refused; rotation would need a restart")
	}
	if !mustVerify(t, auth, "old-token") {
		t.Error("the overlapping old token was refused mid-rotation")
	}

	// Revocation: drop the old token and it stops being accepted, with no
	// restart and no window longer than the reload gate.
	writeTokenFile(t, path, "new-token-value\n")
	clock.advance(2 * time.Second)
	if mustVerify(t, auth, "old-token") {
		t.Error("a token removed from the file is still accepted; a leaked token would be unrevokable")
	}
	if !mustVerify(t, auth, "new-token-value") {
		t.Error("the surviving token was refused after revocation")
	}
}

// TestReloadIsStatGated bounds what a 401 storm can cost: at most one stat per
// gate interval, whatever the request rate.
func TestReloadIsStatGated(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "first\n")

	// First mismatch claims the reload slot and finds nothing new.
	if mustVerify(t, auth, "second-token") {
		t.Fatal("an unknown token was accepted")
	}
	// The file now carries it, but the gate has not elapsed, so the second
	// mismatch must not even stat.
	writeTokenFile(t, path, "first\nsecond-token\n")
	if mustVerify(t, auth, "second-token") {
		t.Error("a reload ran inside the gate window; a 401 storm would stat the file per request")
	}

	clock.advance(2 * time.Second)
	if !mustVerify(t, auth, "second-token") {
		t.Error("the reload did not run after the gate elapsed")
	}
}

// TestUnchangedFileIsNotReread is the cheap branch a wrong-token storm actually
// hits: the gate elapses, the server stats, nothing moved, and it stops there.
// Rewriting the bytes while restoring mtime and size makes a re-read
// observable — if one happened, the new token would be accepted.
func TestUnchangedFileIsNotReread(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "aaaaa\n")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	writeTokenFile(t, path, "bbbbb\n") // same length
	touch(t, path, fi.ModTime())

	clock.advance(2 * time.Second)
	if mustVerify(t, auth, "bbbbb") {
		t.Error("the file was re-read despite an unchanged mtime and size")
	}
	if !mustVerify(t, auth, "aaaaa") {
		t.Error("the cached set was dropped by a reload that should not have happened")
	}
}

// TestBadReloadKeepsTheLastGoodSet: a writer that truncates before writing must
// not lock every client out of the server for the duration of its write. The
// error is reported to the caller so it reaches the log, and the next mismatch
// past the gate re-reads.
func TestBadReloadKeepsTheLastGoodSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, path string)
	}{
		{"truncated mid-write", func(t *testing.T, path string) { writeTokenFile(t, path, "") }},
		{"deleted", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, path, clock := newAuthForTest(t, "live-token\n")
			tc.break_(t, path)
			clock.advance(2 * time.Second)

			ok, err := auth.Verify("unknown")
			if ok {
				t.Error("an unknown token was accepted")
			}
			if err == nil {
				t.Error("a failed reload was silent; the operator gets no log line for a broken token file")
			}
			if !mustVerify(t, auth, "live-token") {
				t.Error("the last-good set was dropped, locking every client out on a writer race")
			}

			// Self-healing: the stat cache was not updated, so the next
			// attempt past the gate re-reads.
			writeTokenFile(t, path, "live-token\nsecond-token\n")
			clock.advance(2 * time.Second)
			if !mustVerify(t, auth, "second-token") {
				t.Error("the reload did not recover after the file was restored")
			}
		})
	}
}

// TestVerifyIsRaceFree hammers the accepted-token fast path and the reload path
// together. Run under -race this is the guard on the atomic swap.
func TestVerifyIsRaceFree(t *testing.T) {
	auth, _, clock := newAuthForTest(t, "good\n")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 200 {
				if i%2 == 0 {
					if ok, _ := auth.Verify("good"); !ok {
						t.Errorf("the accepted token was refused under concurrency")
						return
					}
					continue
				}
				if ok, _ := auth.Verify(fmt.Sprintf("bad-%d-%d", i, j)); ok {
					t.Error("an unknown token was accepted under concurrency")
					return
				}
				clock.advance(400 * time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
}

// TestValidateAuthPosture pins the default: loopback with no token file is
// today's behavior, and every other combination either enables auth or is
// refused at startup. --allow-non-loopback without a credential is the one that
// mattered — reachability was full read and claim authority.
func TestValidateAuthPosture(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		nonLoopback, hasToken, insecureNoAuth bool
		wantErr                               string
	}{
		{name: "loopback, no token: unchanged"},
		{name: "loopback with a token", hasToken: true},
		{name: "non-loopback with a token", nonLoopback: true, hasToken: true},
		{
			name: "non-loopback with no credential", nonLoopback: true,
			wantErr: "--auth-token-file",
		},
		{
			name: "non-loopback waived explicitly", nonLoopback: true, insecureNoAuth: true,
		},
		{
			name: "waiver contradicts a token file", nonLoopback: true, hasToken: true, insecureNoAuth: true,
			wantErr: "--insecure-no-auth",
		},
		{
			name: "waiver on loopback is meaningless", insecureNoAuth: true,
			wantErr: "--allow-non-loopback",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAuthPosture(tc.nonLoopback, tc.hasToken, tc.insecureNoAuth)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateAuthPosture = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateAuthPosture = nil, want a refusal naming %s", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("refusal %q does not name %s, so it does not say how to proceed", err, tc.wantErr)
			}
		})
	}
}

// TestTokenFileAuthNeverHoldsRawTokens: the accepted set in memory is digests.
// A core dump or a heap profile of a running server discloses no credential.
func TestTokenFileAuthNeverHoldsRawTokens(t *testing.T) {
	const secret = "s3cret-token-value"
	auth, _, _ := newAuthForTest(t, secret+"\n")
	if !mustVerify(t, auth, secret) {
		t.Fatal("token refused")
	}
	for _, d := range *auth.digests.Load() {
		if bytes.Contains(d[:], []byte(secret)) {
			t.Error("the cached accepted set carries the raw token")
		}
	}
}
