package httpclient

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
)

// connShareKey is the context key WithSharedConnections stores its memo under.
type connShareKey struct{}

// connShare memoizes the transports the registry opens dial within one scope.
type connShare struct {
	mu    sync.Mutex
	conns map[string]WireClient
}

// WithSharedConnections returns a context under which every registry open
// (NewFromConfig, NewReadOnlyFromConfig) of the same workspace and target reuses
// ONE dialed transport, instead of dialing a fresh one per open
// (bts-xpw.3.14.6, beads-team-server docs/v0-gateway-latency.md option F).
//
// cmd/bd opens an http workspace twice per write: the pre-open identity gate
// previews it, then the command opens it for real. Two transports meant two
// handshakes (GET /v0/beads/context) and, on a gasworks gateway, two credential
// providers — each paying its own sts.Context discovery before its first mint.
// On the hosted /v0 path each of those is ~0.2-0.33 s, sequential. The
// transport carries the handshake cache and the credential provider, so sharing
// it makes the second open free: same server, same identity pin, same
// credential, already verified.
//
// The scope is explicit and per call site, never process-wide: only an open
// whose context carries this memo shares, so a multi-tenant embedder (OpenWith,
// which never reads it) and every test that opens with a plain context keep the
// one-dial-per-open behavior. A shared transport is keyed on the workspace AND
// every target field the dial reads (URL, identity pin, CA file, plaintext
// grant), so a sidecar rewritten between two opens dials afresh.
//
// Sharing alone changes no answer: a shared transport still sends every read.
// WithReadMemo is the separate, narrower opt-in that lets it answer a repeated
// read from the response it already has.
func WithSharedConnections(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(connShareKey{}).(*connShare); ok {
		return ctx
	}
	return context.WithValue(ctx, connShareKey{}, &connShare{conns: map[string]WireClient{}})
}

// dialShared dials target through d, reusing a transport an earlier open under
// the same WithSharedConnections scope already dialed for this workspace and
// target. A failed dial is not memoized.
func dialShared(ctx context.Context, d WireDialer, beadsDir string, target Target) (WireClient, error) {
	share, _ := ctx.Value(connShareKey{}).(*connShare)
	if share == nil {
		return d(ctx, target)
	}
	key := connShareKeyFor(beadsDir, target)
	share.mu.Lock()
	defer share.mu.Unlock()
	if conn, ok := share.conns[key]; ok {
		return conn, nil
	}
	conn, err := d(ctx, target)
	if err != nil {
		return nil, err
	}
	// A transport shared for a one-shot command may also collapse the
	// identical row and settings reads that command repeats (WithReadMemo);
	// any write through it drops them.
	if memoize, _ := ctx.Value(readMemoKey{}).(bool); memoize {
		if m, ok := conn.(readMemoizer); ok {
			m.EnableReadMemo()
		}
	}
	share.conns[key] = conn
	return conn, nil
}

// readMemoizer is the optional transport half WithReadMemo enables;
// *wire.Client (and so *Conn) implements it.
type readMemoizer interface{ EnableReadMemo() }

// readMemoKey is the context key WithReadMemo stores its opt-in under.
type readMemoKey struct{}

// WithReadMemo marks ctx so that a transport a WithSharedConnections scope
// dials under it also memoizes identical single-row and settings reads for the
// rest of its life (wire.Client.EnableReadMemo, wire.ReadMemoTTL;
// bts-xpw.3.14.6). It is for a ONE-SHOT command: one that reads, writes and
// exits within seconds, where an identical re-read is the same question asked
// twice and every write it makes drops the memo first.
//
// A process that outlives its reads must not ask for it. bd serve over an http
// workspace answers other clients, who must see what other writers changed,
// and a --watch poll exists to see exactly those changes; either one answered
// from a memo shows a snapshot up to a TTL old.
//
// It takes effect only inside a WithSharedConnections scope, because sharing is
// what puts every open of the workspace on the one transport whose writes clear
// the memo its reads are answered from. The first open of a shared transport
// decides; the order the two wrappers are applied in does not matter.
func WithReadMemo(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, readMemoKey{}, true)
}

func connShareKeyFor(beadsDir string, target Target) string {
	if abs, err := filepath.Abs(beadsDir); err == nil {
		beadsDir = abs
	}
	base := ""
	if target.BaseURL != nil {
		base = target.BaseURL.String()
	}
	return fmt.Sprintf("%q|%q|%q|%q|%t", beadsDir, base, target.ExpectProjectID, target.CAFile, target.AllowInsecureCredential)
}
