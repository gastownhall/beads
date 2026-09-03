package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Bearer authentication over a file of accepted tokens.
//
// The whole design is one rule: every non-empty line of the file is an accepted
// token, and the file is re-read while the server runs. That single rule is
// what makes rotation hitless and revocation possible without a restart —
// write {new,old}, roll the clients, drop old — and it is why there is no
// second mechanism here. A startup snapshot would instead make rotation a
// process restart and leave a leaked token live for as long as the process is.
//
// There is no token MINTING here and no identity: a token is a shared secret
// that grants the whole surface. Per-client identity, scopes and TLS are
// separate concerns that this file deliberately does not pretend to have.

const (
	// authReloadInterval is the floor between two reload ATTEMPTS, and so both
	// the cost bound and the staleness bound: one stat(2) per interval for the
	// whole server whatever the request rate, and a token added to or removed
	// from the file taking effect within one interval of the next request.
	authReloadInterval = time.Second
	// maxTokenFileBytes refuses to slurp a mis-pointed path. A token file is a
	// handful of lines; anything at this size is a wrong argument, and reading
	// it into memory on every reload would be the actual damage.
	maxTokenFileBytes = 1 << 20
)

// TokenFileAuth verifies presented bearer tokens against a file of accepted
// tokens, one per line, all accepted.
//
// The accepted set is held as SHA-256 digests, never as the tokens themselves,
// so a heap profile or core dump of a running server discloses no credential.
// Verification hashes the presented token and compares digests in constant
// time; hashing both sides also makes the compared lengths uniform, which is
// what subtle.ConstantTimeCompare requires to be constant-time at all.
//
// The zero value is not usable — build one with NewTokenFileAuth.
type TokenFileAuth struct {
	path string
	// reloadGate and now are the two test seams: they let the reload behavior
	// be exercised in milliseconds of fake time instead of real seconds. Both
	// fall back to the constants above when unset.
	reloadGate time.Duration
	now        func() time.Time

	// digests is the last-good accepted set, swapped as a whole. Readers take
	// it with a single atomic load, so the hot path takes no lock and a reload
	// never makes the server briefly accept nothing.
	digests atomic.Pointer[[][sha256.Size]byte]
	// lastAttempt is the unix-nanos of the last reload ATTEMPT — attempt, not
	// success, because the point is to bound the rate at which a storm of
	// unknown tokens can touch the filesystem.
	lastAttempt atomic.Int64

	// mu serializes stat + read + swap. modTime and size are the stat cache
	// that lets an unchanged file cost a stat and no read.
	mu      sync.Mutex
	modTime time.Time
	size    int64
}

// NewTokenFileAuth reads path and returns the verifier for it.
//
// It is deliberately eager and strict: an unreadable, oversized or
// token-less file is an error here rather than a server that starts anyway.
// Both silent alternatives are worse than not starting — failing open serves
// the whole surface with no credential, and failing closed answers 401 to
// every client with nothing on stderr that says why.
func NewTokenFileAuth(path string) (*TokenFileAuth, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("no token file path given")
	}
	a := &TokenFileAuth{path: path}
	digests, modTime, size, err := readTokenFile(path)
	if err != nil {
		return nil, err
	}
	a.digests.Store(&digests)
	a.modTime, a.size = modTime, size
	return a, nil
}

// Verify reports whether token is currently accepted, and returns any error
// from a reload it triggered so the caller can log it. A reload error is not a
// verification result: the last-good set stays in force, and ok is simply the
// answer that set gives.
//
// Freshness is checked BEFORE the comparison and on every call, not only on a
// mismatch, because those two are not the same property. A mismatch-triggered
// reload gives rotation — a client that rolled to a new token 401s once and
// succeeds on the retry — but it cannot give REVOCATION: a token the operator
// has just deleted from the file still matches the cached set, so it triggers
// nothing and stays accepted until some unrelated unknown token happens to
// arrive. Since revocation without a restart is the point, the check runs on
// the accepting path too.
//
// The cost of that is bounded by the same gate, so it is one stat(2) per
// interval for the whole server whatever the request rate — and an unchanged
// file stops at the stat, so the steady state reads nothing. There is no
// background goroutine and no work at all while the server is idle: the check
// rides the requests that care about it.
func (a *TokenFileAuth) Verify(token string) (ok bool, reloadErr error) {
	if token == "" {
		return false, nil
	}
	// A caller that loses the gate to a concurrent reload compares against the
	// set that reload is about to replace, rather than blocking on it. That is
	// a bounded staleness of one gate interval, which is the window this whole
	// mechanism is specified in; blocking every request behind one stat is not
	// a trade worth making.
	if a.claimReloadSlot() {
		reloadErr = a.reload()
	}
	return a.accepts(sha256.Sum256([]byte(token))), reloadErr
}

// accepts compares want against every cached digest with no early exit, so the
// time taken says nothing about WHICH token was closest.
func (a *TokenFileAuth) accepts(want [sha256.Size]byte) bool {
	match := 0
	for _, d := range *a.digests.Load() {
		match |= subtle.ConstantTimeCompare(d[:], want[:])
	}
	return match == 1
}

// claimReloadSlot admits at most one reload attempt per gate interval across
// all goroutines. It records the ATTEMPT, not the success, because what needs
// bounding is how often the filesystem is touched. The compare-and-swap is
// what makes a loser return immediately instead of queueing behind the
// winner's stat.
func (a *TokenFileAuth) claimReloadSlot() bool {
	gate := a.reloadGate
	if gate <= 0 {
		gate = authReloadInterval
	}
	clock := a.now
	if clock == nil {
		clock = time.Now
	}
	now := clock().UnixNano()
	last := a.lastAttempt.Load()
	if now-last < int64(gate) {
		return false
	}
	return a.lastAttempt.CompareAndSwap(last, now)
}

// reload re-reads the token file if it has changed, and swaps the accepted set.
//
// A failed read or a file that parses to zero tokens KEEPS the last-good set
// and reports the error. A writer that truncates before writing must not lock
// every client out of the server for the duration of its write — and because
// the stat cache is only updated on success, the next attempt past the gate
// re-reads and recovers by itself. (Operators should still write the file
// atomically, temp file plus rename; a Kubernetes secret mount already does.)
func (a *TokenFileAuth) reload() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	fi, err := os.Stat(a.path)
	if err != nil {
		return fmt.Errorf("token file %s: %w", a.path, err)
	}
	if fi.ModTime().Equal(a.modTime) && fi.Size() == a.size {
		return nil
	}
	digests, modTime, size, err := readTokenFile(a.path)
	if err != nil {
		return err
	}
	a.digests.Store(&digests)
	a.modTime, a.size = modTime, size
	return nil
}

// readTokenFile reads and parses the whole file, returning the stat it read it
// under so the caller's cache describes the bytes it actually holds.
func readTokenFile(path string) (digests [][sha256.Size]byte, modTime time.Time, size int64, err error) {
	// #nosec G304 -- opening an operator-named path IS the feature: the path
	// comes from --auth-token-file (or its BEADS_SERVE_* alias), which only the
	// person starting the process supplies. No request influences it.
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("token file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("token file %s: %w", path, err)
	}
	if fi.IsDir() {
		return nil, time.Time{}, 0, fmt.Errorf("token file %s is a directory", path)
	}

	// One byte past the cap, so an oversized file is detected rather than
	// silently truncated into an accepted set missing its last token.
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("token file %s: %w", path, err)
	}
	if len(data) > maxTokenFileBytes {
		return nil, time.Time{}, 0, fmt.Errorf("token file %s is larger than %d bytes; that is a mis-pointed path, not a token file",
			path, maxTokenFileBytes)
	}

	digests = parseTokens(data)
	if len(digests) == 0 {
		return nil, time.Time{}, 0, fmt.Errorf("token file %s contains no tokens", path)
	}
	return digests, fi.ModTime(), fi.Size(), nil
}

// parseTokens turns the file into the accepted digest set: every non-empty
// line, trimmed, is one token. Trimming handles CRLF and stray indentation; no
// comment syntax is invented, because a "#" line would be a token an operator
// believed was disabled.
func parseTokens(data []byte) [][sha256.Size]byte {
	var out [][sha256.Size]byte
	for line := range strings.SplitSeq(string(data), "\n") {
		tok := strings.TrimSpace(line)
		if tok == "" {
			continue
		}
		out = append(out, sha256.Sum256([]byte(tok)))
	}
	return out
}

// ValidateAuthPosture refuses the flag combinations that would ship a server
// nobody meant to ship. It is called from the CLI so the refusal is immediate,
// and again from Listen so the library cannot be misused by a second caller.
//
// The rule the whole slice turns on: loopback with no token file is today's
// behavior, byte for byte. Everything else is either authenticated or refused,
// with one explicitly-named escape hatch.
func ValidateAuthPosture(allowNonLoopback, hasTokenFile, insecureNoAuth bool) error {
	if insecureNoAuth && hasTokenFile {
		return errors.New("--insecure-no-auth contradicts --auth-token-file; pass one or the other")
	}
	if insecureNoAuth && !allowNonLoopback {
		return errors.New("--insecure-no-auth applies only to a bind beyond loopback; on loopback there is nothing to waive, so pass --allow-non-loopback or drop the flag")
	}
	if allowNonLoopback && !hasTokenFile && !insecureNoAuth {
		return errors.New("--allow-non-loopback requires --auth-token-file (or the explicit --insecure-no-auth): every peer that can reach the address gets full read and claim access")
	}
	return nil
}
