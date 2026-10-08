// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/ca.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/idna"
)

// CAFileEnv names a PEM file that becomes the ENTIRE trusted root pool for the
// ONE target it names — not an addition to the system store, and not applied
// to any other target this process happens to dial.
//
// It exists for a private CA with no name constraints (the Gas City Beads
// Serve CA): a CA like that can mint a certificate for ANY hostname, so
// trusting it system-wide, or via SSL_CERT_FILE, would let it vouch for every
// TLS connection the process makes, not just the one bd serve it was issued
// for. TransportFor instead builds a pool that contains ONLY this CA and scopes
// it to one target's *http.Client.
//
// # Syntax is host-scoped: host[:port]=path
//
// The value is "host[:port]=path", e.g. "serve.example.com=/etc/bd/ca.pem" or
// "serve.example.com:8443=/etc/bd/ca.pem". A pattern with no port matches a
// target's host regardless of its own port; a pattern with a port matches only
// that exact host:port. A malformed value (no "=", or nothing on either side
// of it) is refused outright — including the old bare-path form this
// superseded, which applied to every target the process dialed regardless of
// host, the impersonation risk this syntax exists to close. A well-formed
// value whose host does not match a given target is silently not applied to
// that target: it falls through to the sidecar (or system roots), never to
// the env's CA.
//
// # Precedence: env beats sidecar, but never silently
//
// When the env var's host pattern matches a target, it wins over that
// target's http_target.json "ca_file" field (Target.CAFile, written by
// `bd connect --ca-file <path>`) — the same rung order the bearer ladder
// already uses (BEADS_HTTP_TOKEN beats the credentials file, see
// credential.go): the environment is the operator's per-invocation override,
// and the sidecar is per-user state persisted once at connect time. But
// unlike the bearer ladder, an env value that matches a target AND disagrees
// with that target's own sidecar ca_file is a hard refusal, not a silent
// override: the two rungs naming different CAs for the same target is very
// likely a stale sidecar or a misdirected env var, and picking one silently
// would trust whichever file the operator did NOT intend.
//
// # The embedder door does not read this at all
//
// This env var is the CLI's own per-invocation override and is consulted only
// by the automatic dial path (DialWith, when the caller leaves
// DialOptions.HTTPClient nil) — the path `bd connect`, `bd ready`, and every
// other cmd/bd invocation rides. backend/http's public TransportFor (the
// embedder door gc calls explicitly to compose a transport into its own
// *http.Client) never reads it: an embedder juggling many targets in one
// process must not have any of them silently renarrowed by an ambient
// variable a completely unrelated invocation exported, so it always builds
// from Target.CAFile alone. See TransportForFile.
//
// #nosec G101 -- the NAME of an environment variable, not a credential.
const CAFileEnv = "BEADS_HTTP_CA_FILE"

// maxIdleConnsPerHost raises TransportFor's per-host idle pool past
// http.DefaultTransport's default of 2. A CA-scoped target is, by
// construction, one host — that's the whole shape this feature exists for —
// so a concurrency-8 caller (gc's ready-veto fan-out) hammering it legitimately
// wants more than 2 warm connections; below that ceiling, every request past
// the second pays a fresh TLS handshake instead of reusing one already paid
// for. See TransportFor's "Idle connection reuse" doc. It applies equally to
// every http-backend target, CA-configured or not (see baselineTransport in
// dial.go): the RTT cost of an under-pooled host is the same either way.
const maxIdleConnsPerHost = 16

// maxCAFileBytes caps how large a configured CA file may be. A CA bundle is
// operator-owned configuration, not arbitrary input, and legitimate ones are a
// handful of certificates — a few KiB. A file past this ceiling is refused
// rather than parsed: it is almost certainly the wrong file (a full trust
// store, a log, something else entirely pointed at by mistake), and PEM
// decoding an unbounded file is needless work for something that is never a
// deliberate CA bundle.
const maxCAFileBytes = 1 << 20 // 1 MiB

// resolvedCA is what resolveCAFile found: the file to trust for a target, and
// which knob it came from, so a refusal names the thing to fix rather than an
// unqualified "ca file".
type resolvedCA struct {
	path  string
	label string
}

// configured reports whether a CA applies at all; the zero value means
// "system roots", the same signal TransportFor's nil, nil return carries.
func (r resolvedCA) configured() bool { return r.path != "" }

// parseCAFileEnv parses CAFileEnv's host-scoped syntax, "host[:port]=path".
// Anything else — no "=", an empty host, an empty path, or a path that is
// not absolute — is malformed and refused: there is no reasonable default
// scope for a value this package cannot parse, and falling back to "applies
// everywhere" is exactly the vulnerability this syntax replaces. The split
// is on the FIRST "=", not the last: a path may itself contain "=" (rare,
// but legal in a POSIX filename), and splitting on the last one would silently
// mis-parse the host pattern in that case rather than refusing loudly.
// Requiring the path be absolute closes a smaller but real ambiguity: a
// relative path's meaning would depend on the process's current directory at
// resolution time, which the sidecar and --ca-file paths never do.
func parseCAFileEnv(raw string) (hostPattern, path string, err error) {
	eq := strings.Index(raw, "=")
	if eq <= 0 || eq == len(raw)-1 {
		return "", "", fmt.Errorf(
			"%s %q: want host[:port]=path (e.g. serve.example.com=/etc/bd/ca.pem, or serve.example.com:8443=/etc/bd/ca.pem); a bare path is no longer accepted because it applied to every target this process dials",
			CAFileEnv, raw)
	}
	hostPattern = strings.TrimSpace(raw[:eq])
	path = strings.TrimSpace(raw[eq+1:])
	if hostPattern == "" || path == "" {
		return "", "", fmt.Errorf("%s %q: want host[:port]=path", CAFileEnv, raw)
	}
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("%s %q: path %q must be absolute", CAFileEnv, raw, path)
	}
	if strings.HasSuffix(hostPattern, ":") {
		// "h.example:" (or "[::1]:") names a colon with nothing after it — a
		// stray trailing colon, almost certainly a dropped port rather than
		// a deliberate pattern, and NEITHER of the two meanings a colon can
		// carry here (no colon at all: any port; host:port: exactly one):
		// silently picking either would be a guess, not a parse.
		return "", "", fmt.Errorf("%s %q: host pattern %q ends with \":\" and no port; want host, or host:port", CAFileEnv, raw, hostPattern)
	}
	return hostPattern, path, nil
}

// caHostMatches reports whether pattern (the host half of a parsed
// CAFileEnv value) names target. Both sides are IDNA/punycode-normalized to
// ASCII (so "café.example" and "xn--caf-dma.example" agree, whichever form
// an operator typed) and lowercased, with a trailing dot stripped, before
// comparison, and ports are normalized against target's own URL scheme
// (https defaults to 443, http to 80) before being compared, so "host" and
// "host:443" agree for an https target exactly as they would if the
// target's URL had spelled the port out. A pattern with no port matches
// target's host regardless of target's own port — so "example.com=..."
// applies whether the target dials :443 or :8443 — but a pattern WITH a
// port matches only that exact, normalized host:port. IPv6 literals ("[::1]"
// or "[::1]:8443") are handled the same way net/url and net.SplitHostPort
// do. A host that fails IDNA normalization on either side is reported as an
// error rather than silently compared as raw text: an unnormalizable
// pattern could otherwise disagree with a target that a browser or the
// server's own cert treats as the same name.
func caHostMatches(pattern string, target Target) (bool, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || target.BaseURL == nil {
		return false, nil
	}
	patternHost, patternPort, err := splitCAHostPort(pattern, "")
	if err != nil {
		return false, fmt.Errorf("host pattern %q: %w", pattern, err)
	}
	targetHost, targetPort, err := splitCAHostPort(target.BaseURL.Host, target.BaseURL.Scheme)
	if err != nil {
		return false, fmt.Errorf("target host %q: %w", target.BaseURL.Host, err)
	}
	if patternHost == "" || targetHost == "" || patternHost != targetHost {
		return false, nil
	}
	if patternPort == "" {
		return true, nil
	}
	return patternPort == targetPort, nil
}

// caIDNAProfile normalizes a hostname label to lowercase ASCII/punycode
// without the stricter registration-validity checks idna.Lookup would apply
// (rejecting labels a real, already-issued certificate's SAN can legally
// contain, e.g. a bare underscore label, would turn a working pattern into a
// refusal for no security reason) — this comparison only needs both sides
// folded to the SAME ASCII form, not a verdict on whether the name could be
// newly registered.
var caIDNAProfile = idna.New(idna.MapForLookup(), idna.Transitional(true))

// splitCAHostPort splits hostport into an IDNA/punycode-normalized,
// lowercased, trailing-dot-stripped host and its port, handling a bracketed
// IPv6 literal with or without a port. When hostport carries no port and
// scheme names a well-known one (http, https), the returned port defaults to
// that scheme's own — the normalization caHostMatches uses so an explicit
// ":443" pattern still matches an https target whose URL never spells the
// default port out. An IPv6 literal (bracketed, or one that fails ASCII
// hostname normalization for any other reason) is returned lowercased but
// otherwise unnormalized: idna.ToASCII on a literal address is a no-op or an
// error depending on form, and this package already compares IPv6 literals
// as plain text.
func splitCAHostPort(hostport, scheme string) (host, port string, err error) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", "", nil
	}
	isIPv6Literal := false
	if h, p, splitErr := net.SplitHostPort(hostport); splitErr == nil {
		host, port = h, p
	} else if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	} else {
		host = hostport
	}
	host = strings.TrimSuffix(host, ".")
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if net.ParseIP(host) != nil {
		isIPv6Literal = true
	}
	if isIPv6Literal {
		host = strings.ToLower(host)
	} else {
		ascii, asciiErr := caIDNAProfile.ToASCII(host)
		if asciiErr != nil {
			return "", "", fmt.Errorf("host %q is not valid ASCII or a valid internationalized hostname: %w", host, asciiErr)
		}
		host = strings.ToLower(ascii)
	}
	if port == "" {
		switch strings.ToLower(scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return host, port, nil
}

// sameCAFile reports whether a and b name the same file: identical after
// filepath.Abs+Clean, or — when both exist — the same file by os.SameFile
// (a hardlink or bind-mount alias). This replaces a raw string compare in
// resolveCAFile's env-vs-sidecar disagreement check, which would otherwise
// refuse two paths that plainly name the same file (one relative, one
// absolute; one with a trailing "/./"; a symlink alias) purely because their
// text differs.
func sameCAFile(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return a == b
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	absA, absB = filepath.Clean(absA), filepath.Clean(absB)
	if absA == absB {
		return true
	}
	infoA, errA2 := os.Stat(absA)
	infoB, errB2 := os.Stat(absB)
	if errA2 == nil && errB2 == nil {
		return os.SameFile(infoA, infoB)
	}
	return false
}

// resolveCAFile applies the env-over-sidecar precedence documented on
// CAFileEnv, now host-scoped: a "" path in the result means system roots
// apply, unchanged from today, for a target that matches no configured CA.
func resolveCAFile(target Target) (resolvedCA, error) {
	sidecar := strings.TrimSpace(target.CAFile)
	raw := strings.TrimSpace(os.Getenv(CAFileEnv))
	if raw == "" {
		if sidecar == "" {
			return resolvedCA{}, nil
		}
		return resolvedCA{path: sidecar, label: "ca_file"}, nil
	}

	pattern, envPath, err := parseCAFileEnv(raw)
	if err != nil {
		return resolvedCA{}, err
	}

	matches, err := caHostMatches(pattern, target)
	if err != nil {
		return resolvedCA{}, fmt.Errorf("%s %q: %w", CAFileEnv, raw, err)
	}
	if !matches {
		if sidecar == "" {
			return resolvedCA{}, nil
		}
		return resolvedCA{path: sidecar, label: "ca_file"}, nil
	}

	if sidecar != "" && !sameCAFile(sidecar, envPath) {
		return resolvedCA{}, fmt.Errorf(
			"%s (%s=...) names %q for %s, but its sidecar ca_file names %q for the same target; remove one so the CA used here is unambiguous",
			CAFileEnv, pattern, envPath, target, sidecar)
	}
	return resolvedCA{path: envPath, label: CAFileEnv}, nil
}

// TransportFor builds the http.RoundTripper that trusts ONLY the CA named by
// target's resolved, host-scoped setting (CAFileEnv when its host pattern
// matches target, else Target.CAFile), REPLACING rather than extending the
// system root pool. It returns nil, nil when neither is configured — the
// caller's signal to keep using the default, system-roots transport, which is
// what preserves today's behavior for loopback dev and every target that has
// never set a CA.
//
// This is exported so a caller that builds its own *http.Client for other
// reasons — a tuned timeout, a proxy — can still pick up the automatic,
// env-aware resolution DialWith itself uses: compose the returned
// RoundTripper into that client's Transport field. DialWith calls this itself
// when the caller left DialOptions.HTTPClient nil, so cmd/bd's own dial path
// (the default dialer, the gateway dialer, and `bd connect`'s own verifying
// handshake) needs no further wiring.
//
// An embedder outside this module that wants a transport from an EXPLICIT
// file, ignoring CAFileEnv entirely, should call TransportForFile(target.CAFile)
// instead — see that function and backend/http's public door.
//
// The returned transport never sets ServerName: TLS's SNI and the Host header
// both continue to come from the request's URL, exactly as they do for the
// default transport, so this only narrows which root certificates are
// trusted — it never changes which host a request addresses.
//
// Errors refuse loudly rather than fall back to system roots: a missing file,
// an unreadable one, a malformed CAFileEnv value, a hygiene violation (file
// permissions, size), or one with no valid PEM-encoded certificate is a hard
// error, never a silent downgrade to public trust or to no trust check at
// all.
//
// # Idle connection reuse
//
// A CA-scoped target is still a single host, so it gets the same
// keep-alive budget raised elsewhere for this reason: http.DefaultTransport's
// MaxIdleConnsPerHost is 2, which is too small for a concurrency-8 burst
// against one target — anything past the first 2 idle connections pays a
// fresh TLS handshake, tens of milliseconds at a typical cross-region RTT.
// maxIdleConnsPerHost raises that ceiling so a warm burst reuses connections
// instead of re-handshaking per request.
//
// # Caching and root rotation
//
// The transport returned for a given absolute path is cached and reused for
// the life of the process, keyed by (absolute path, file content hash), so a
// long-lived process dialing the same target repeatedly reuses one transport
// and its warm connection pool rather than paying a fresh TLS handshake and
// leaking the previous transport's idle connections on every dial. A CHANGED
// file at the same path — a different content hash — produces a fresh
// transport, and the previous one's idle connections are closed. Ship a root
// rotation as a bundle containing BOTH the old and the new CA (a
// multi-certificate PEM, one CERTIFICATE block per certificate): every
// process, however long-lived, keeps verifying through the rotation, and
// converges on trusting only the new CA once the bundle is trimmed back down
// to it — with no window where some processes trust only the old certificate
// and others only the new one.
//
// Because it is cached, the returned transport is SHARED: every caller in
// this process asking for the same file holds the same *http.Transport. Treat
// it as read-only — wrap it rather than type-asserting and reconfiguring it —
// since setting its TLSClientConfig, Proxy or DialContext would silently
// change trust for every other holder.
//
// # HTTPS proxies
//
// When a CA is configured, the returned transport refuses to reach a target
// through an https:// proxy — see caAwareProxy. A plain http:// proxy is
// unaffected: the destination's TLS is still verified against the configured
// CA end to end through it.
func TransportFor(target Target) (http.RoundTripper, error) {
	resolved, err := resolveCAFile(target)
	if err != nil {
		return nil, err
	}
	if !resolved.configured() {
		return nil, nil
	}
	return transportForFile(resolved.path, resolved.label)
}

// TransportForFile builds the http.RoundTripper that trusts ONLY the CA at
// path, REPLACING rather than extending the system root pool — the same kind
// of transport TransportFor builds, but for a path the caller already
// resolved itself, with NO CAFileEnv involvement at all.
//
// It exists for two callers that must not go through env resolution:
//
//   - A `bd connect --ca-file X`-style command verifies the server with X
//     BEFORE writing anything; if CAFileEnv happened to be set to a
//     different, valid CA for the same host, resolving through the normal
//     env-aware path would verify against the WRONG file relative to what
//     --ca-file names, then write X unchecked.
//   - backend/http's public TransportFor (the embedder door) builds from
//     Target.CAFile alone, because CAFileEnv is this process's ambient,
//     per-invocation override for the CLI's own dial path, and an embedder
//     juggling several targets in one process must not have any of them
//     silently renarrowed by a variable it may not even know exists.
//
// Like TransportFor, this participates in the (absolute path, content hash)
// transport cache — so what it returns is the same shared, read-only
// transport — and refuses loudly (missing file, hygiene, malformed PEM)
// rather than falling back to system roots.
func TransportForFile(path string) (http.RoundTripper, error) {
	return transportForFile(path, "ca_file")
}

// cachedTransport pairs a built transport with the content hash of the file
// it was built from and the sequence number of the read that produced it, so
// a later call can tell a cache hit from a rotation, and a stale concurrent
// read from a genuine one.
//
// seq, not the file's mtime, orders reads: a rotation landed by cp -p,
// rsync -t, or tar can preserve (or coincidentally produce) the SAME mtime
// as the file it replaces, down to whatever resolution the filesystem
// keeps — a same-second write is common, not exotic. Comparing mtimes then
// treats a genuine rotation as "no newer than what's cached" and keeps
// serving the superseded (possibly revoked) transport forever, since the
// file's mtime never again exceeds the stale cached one. seq instead counts
// which read of the file started first, a purely in-process fact
// transportReadSeq's own monotonic counter can always order correctly,
// whatever the filesystem clock says.
type cachedTransport struct {
	hash      [32]byte
	seq       uint64
	transport *http.Transport
}

// maxCachedTransports bounds the process-wide transport cache. Each entry
// pins one *http.Transport and its idle connection pool for the life of the
// process; an unbounded cache keyed by absolute path would let a caller that
// churns through many distinct CA file paths over a long-lived process's
// life (many short-lived tenants, an ephemeral-workspace-heavy CI runner)
// grow it without limit. 64 comfortably covers every legitimate shape this
// package expects — one process rarely dials more than a handful of distinct
// CA-scoped hosts.
const maxCachedTransports = 64

var (
	transportCacheMu sync.Mutex
	transportCache   = map[string]*cachedTransport{}
	// transportCacheOrder tracks least-recently-used first; touched under
	// transportCacheMu alongside transportCache itself.
	transportCacheOrder []string
	// transportReadSeq is the monotonic counter behind cachedTransport.seq,
	// incremented under transportCacheMu immediately BEFORE the file read it
	// tags begins — never after, and never derived from the file's own
	// mtime; see cachedTransport's doc.
	transportReadSeq uint64
)

// transportRecord is what transportProvenance remembers about one
// *http.Transport this package built: the absolute path and content hash it
// was built from, at build time.
type transportRecord struct {
	path string
	hash [32]byte
}

// transportProvenance maps a *http.Transport this package built to the
// transportRecord it was built from, keyed by the transport's own pointer
// identity. Unlike transportCache (bounded, LRU-evicted, keyed by path),
// this is NEVER evicted and never keyed by path: a caller that obtained a
// transport from TransportFor/TransportForFile and held onto it is entitled
// to keep using it for as long as the file it names is unchanged, even
// after churn through many other CA paths has pushed its cache entry (or a
// later rotation's entry for the same path) out of transportCache entirely.
// matchesCachedTransport consults this, then re-reads and re-hashes the
// file's CURRENT content, so eviction alone never causes a false refusal
// and a rotation is never silently trusted just because the pointer once
// belonged here.
//
// This does grow by one entry per transport ever built, for the life of the
// process — an intentional, deliberately unbounded trade against the
// alternative (a bounded/evicted provenance table would reintroduce exactly
// the false-refusal-on-eviction bug this exists to close). A process that
// churns through many thousands of distinct CA files over its lifetime
// leaks a few dozen bytes per rotation; there is no plausible deployment of
// this feature where that outweighs the correctness this buys.
var transportProvenance sync.Map // map[*http.Transport]transportRecord

// errCAFileChangedSinceTransportBuilt is matchesCachedTransport's sentinel
// for "this IS a transport we built, but the file has changed since" — kept
// distinct from "never heard of this transport at all" so DialWith can give
// a caller a clear, specific instruction (rebuild it) instead of the generic
// unrecognized-transport refusal.
var errCAFileChangedSinceTransportBuilt = errors.New("CA file changed since this transport was built; rebuild via TransportFor or TransportForFile")

// matchesCachedTransport reports whether rt is a *http.Transport this
// package built for path, AND that the file at path still has the exact
// content it was built from — the same instance TransportFor or
// TransportForFile would return for it right now, or would have returned
// for it at some point before path's cache entry was evicted or replaced by
// a later rotation. DialWith uses this to recognize the documented embedder
// recipe (backend/http.TransportFor(target) composed into the caller's own
// *http.Client) as trustworthy rather than refusing it outright: the caller
// built its transport through this package's own CA-scoped constructor, so
// there is nothing left to verify beyond "has the file since changed". A
// transport built any other way — even one that happens to trust the right
// CA, even one this package built for a DIFFERENT path — does not match,
// because this package cannot know what else the caller may have layered
// onto an unrelated transport.
//
// A non-nil error (other than errCAFileChangedSinceTransportBuilt, checked
// via errors.Is) means the current file could not be read at all — a
// distinct failure from "does not match", which DialWith surfaces rather
// than treating as a silent refusal indistinguishable from an unrelated
// transport.
func matchesCachedTransport(rt http.RoundTripper, path string) (bool, error) {
	if rt == nil || path == "" {
		return false, nil
	}
	t, ok := rt.(*http.Transport)
	if !ok {
		return false, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, nil
	}
	recVal, ok := transportProvenance.Load(t)
	if !ok {
		return false, nil
	}
	rec, ok := recVal.(transportRecord)
	if !ok || rec.path != abs {
		return false, nil
	}
	// Re-read and re-hash the file's CURRENT content: recognizing the
	// pointer alone would accept a transport built from a CA that has since
	// been rotated (or revoked) out from under it, whether or not this
	// path's cache entry still names this same transport.
	data, err := readCAFile(abs, "ca_file")
	if err != nil {
		return false, err
	}
	if sha256.Sum256(data) != rec.hash {
		return false, errCAFileChangedSinceTransportBuilt
	}
	return true, nil
}

// transportForFile is the shared build behind TransportFor and
// TransportForFile: resolve path to an absolute one, read and hygiene-check
// it, and either reuse the cached transport for that (path, content) pair or
// build and cache a fresh one. label names which setting (CAFileEnv or
// "ca_file") a refusal should point at.
func transportForFile(path, label string) (http.RoundTripper, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%s %q: resolve absolute path: %w", label, path, err)
	}

	// Claim this read's sequence number BEFORE doing any file I/O, under the
	// same lock the cache itself is guarded by: this is what lets two
	// concurrent reads be ordered correctly regardless of what the
	// filesystem's clock says about either one's mtime. See
	// cachedTransport's doc.
	transportCacheMu.Lock()
	transportReadSeq++
	mySeq := transportReadSeq
	transportCacheMu.Unlock()

	data, err := readCAFile(abs, label)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)

	transportCacheMu.Lock()
	if cached, ok := transportCache[abs]; ok && cached.hash == hash {
		touchTransportCacheLocked(abs)
		t := cached.transport
		transportCacheMu.Unlock()
		return t, nil
	}
	transportCacheMu.Unlock()

	pool, err := parseCAPool(data, abs, label)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck // http.DefaultTransport is always *http.Transport
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	transport.MaxIdleConnsPerHost = maxIdleConnsPerHost
	transport.Proxy = caAwareProxy

	transportCacheMu.Lock()
	defer transportCacheMu.Unlock()

	if cached, ok := transportCache[abs]; ok {
		if cached.hash == hash {
			// Another goroutine built and cached this exact content while we
			// were building ours; reuse theirs. Ours was never assigned to
			// any connection, so there is nothing on it to close.
			touchTransportCacheLocked(abs)
			return cached.transport, nil
		}
		if cached.seq > mySeq {
			// A read that claimed its sequence number AFTER ours already
			// landed a different (necessarily newer) hash. Never let a read
			// that started earlier clobber a cache entry from one that
			// started later just because its content hash differs — the
			// file may have been rewritten again between this read's open
			// and this lock, and ordering by seq (not mtime) is exactly
			// what stays correct under a same-second rewrite.
			touchTransportCacheLocked(abs)
			return cached.transport, nil
		}
		// Genuine rotation: close the outgoing transport's idle connections
		// so they do not leak, then replace it.
		cached.transport.CloseIdleConnections()
	}
	transportCache[abs] = &cachedTransport{hash: hash, seq: mySeq, transport: transport}
	transportProvenance.Store(transport, transportRecord{path: abs, hash: hash})
	touchTransportCacheLocked(abs)
	evictOverCapacityLocked()
	return transport, nil
}

// touchTransportCacheLocked moves abs to the most-recently-used end of
// transportCacheOrder. Callers must hold transportCacheMu.
func touchTransportCacheLocked(abs string) {
	for i, k := range transportCacheOrder {
		if k == abs {
			transportCacheOrder = append(transportCacheOrder[:i], transportCacheOrder[i+1:]...)
			break
		}
	}
	transportCacheOrder = append(transportCacheOrder, abs)
}

// evictOverCapacityLocked closes and drops the least-recently-used entries
// until the cache is back within maxCachedTransports. Callers must hold
// transportCacheMu.
func evictOverCapacityLocked() {
	for len(transportCacheOrder) > maxCachedTransports {
		oldest := transportCacheOrder[0]
		transportCacheOrder = transportCacheOrder[1:]
		if cached, ok := transportCache[oldest]; ok {
			cached.transport.CloseIdleConnections()
			delete(transportCache, oldest)
		}
	}
}

// caFileReadHookForTest, when non-nil, runs immediately after
// checkCAFilePermissions succeeds and BEFORE the file is opened for reading
// — the exact TOCTOU window the O_NOFOLLOW-open-then-fstat/SameFile guard in
// readCAFile below exists to close. Production never sets it; a test sets it
// to swap the file (or replace it with a symlink) at exactly that point and
// confirms the read is refused rather than silently trusting whatever
// replaced it.
var caFileReadHookForTest func(real string)

// readCAFile reads path after checking its hygiene (permissions, ownership,
// every symlink hop's containing directory) and confirming that the file it
// actually reads is provably the same one that hygiene check examined,
// returning its content. path must already be absolute; label names the
// setting a refusal should point at.
//
// checkCAFilePermissions resolves path to a real, non-symlink file (checking
// every symlink hop's own containing directory along the way, not just the
// final target's) and returns both that real path and the os.FileInfo it
// stat'd for it. Between that check and this function's own open, an
// attacker who can still write somewhere in the resolved path could swap the
// leaf out from under it; this function's own open uses O_NOFOLLOW — so a
// swap for a symlink is refused outright — and then fstats the opened
// descriptor and compares it against the checked FileInfo with os.SameFile,
// so a swap for a DIFFERENT regular file (same name, same directory) is also
// caught before a single byte is trusted: reading is always from the fd just
// opened, never from a second, independent path lookup.
func readCAFile(path, label string) ([]byte, error) {
	real, checked, err := checkCAFilePermissions(path)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", label, path, err)
	}

	if caFileReadHookForTest != nil {
		caFileReadHookForTest(real)
	}

	// #nosec G304 -- path is operator-provided configuration
	// (BEADS_HTTP_CA_FILE, or the per-user http_target.json sidecar written by
	// `bd connect --ca-file`), not attacker-controlled input; its permissions
	// were just checked above, and openCAFileNoFollow additionally refuses to
	// open real at all if it has become a symlink since that check (unix).
	f, err := openCAFileNoFollow(real)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", label, path, err)
	}
	defer f.Close() //nolint:errcheck // read-only fd; nothing to flush on close

	opened, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", label, path, err)
	}
	if !os.SameFile(checked, opened) {
		return nil, fmt.Errorf("%s %q: changed between the permission check and the read; refusing rather than trust whatever replaced it", label, path)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxCAFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", label, path, err)
	}
	if len(data) > maxCAFileBytes {
		return nil, fmt.Errorf("%s %q: larger than %d bytes; a CA bundle this size is almost certainly the wrong file", label, path, maxCAFileBytes)
	}
	return data, nil
}

// parseCAPool decodes data as a sequence of PEM blocks and requires EVERY
// block to be a well-formed CERTIFICATE. A block of any other type, or one
// that fails to parse, refuses the WHOLE file rather than silently keeping
// whatever did parse — crypto/x509.CertPool.AppendCertsFromPEM's behavior,
// which this deliberately does not use. A CA file is small, operator-owned
// configuration, so a block it cannot vouch for (a private key pasted in by
// accident, a corrupted certificate) is far more likely a mistake than an
// intentional block meant to be skipped, and accepting the file anyway would
// trust a pool the operator never actually reviewed.
//
// A single LEAF certificate — no CA:true, no key-cert-sign usage — still
// works here: TLS root-of-trust verification only requires the chain to
// terminate at a certificate in RootCAs, so a file holding just the server's
// own leaf is a valid way to pin that one certificate rather than a CA. It is
// operationally brittle (it must be replaced on every renewal), but this
// package does not refuse it: pinning is a legitimate, if narrower, use of
// this same mechanism.
func parseCAPool(data []byte, path, label string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	rest := data
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		n++
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%s %q: PEM block %d is %q, not CERTIFICATE; only certificates (a CA, or a single leaf to pin) belong in this file", label, path, n, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s %q: PEM block %d does not parse as a certificate: %w", label, path, n, err)
		}
		pool.AddCert(cert)
	}
	if n == 0 {
		return nil, fmt.Errorf("%s %q: no PEM certificates found", label, path)
	}
	return pool, nil
}

// caAwareProxy is the CA-scoped transport's Proxy func. For a plain http://
// proxy it behaves exactly like http.ProxyFromEnvironment: the destination
// host's own TLS is still verified end to end against this transport's
// narrow RootCAs, so routing through a plaintext-to-the-proxy CONNECT tunnel
// changes nothing about what gets trusted.
//
// It refuses an https:// proxy instead of dialing it. The CONNECT to the
// proxy ITSELF would be a TLS handshake made with this same TLSClientConfig,
// so the proxy's own certificate — issued by whatever CA actually vouches for
// it, almost never the single private CA this transport was scoped to — would
// be checked against that narrow pool and (correctly) fail, or, worse, if it
// happened to validate, Proxy-Authorization would be sent across a connection
// verified by the wrong trust anchor. Refusing up front is clearer than
// either outcome.
//
// This reads the proxy configuration via golang.org/x/net/http/httpproxy
// directly rather than http.ProxyFromEnvironment: the latter caches the
// environment the first time ANY transport in the process resolves a proxy,
// behind a package-level sync.Once, which would make this refusal depend on
// unrelated dial or test ordering. httpproxy.FromEnvironment reads the
// environment fresh on every call.
func caAwareProxy(req *http.Request) (*url.URL, error) {
	proxyURL, err := httpproxy.FromEnvironment().ProxyFunc()(req.URL)
	if err != nil {
		return nil, err
	}
	if proxyURL != nil && proxyURL.Scheme == "https" {
		return nil, fmt.Errorf(
			"refusing to reach %s through an https:// proxy (%s) while a CA is configured for this target: the proxy's own TLS would be checked against that CA instead of the system roots that actually vouch for it; use an http:// proxy instead (the destination's TLS is still verified against the configured CA through it), or exclude this host via NO_PROXY",
			req.URL.Host, proxyURL.Redacted())
	}
	return proxyURL, nil
}
