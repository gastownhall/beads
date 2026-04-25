package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// stubConfigStore satisfies storage.Storage at the type level via embedding.
// It overrides GetConfig to return canned values; any other method invocation
// would panic on the embedded nil — tests must only exercise GetConfig.
type stubConfigStore struct {
	storage.Storage
	value string
	err   error
}

func (s *stubConfigStore) GetConfig(_ context.Context, _ string) (string, error) {
	return s.value, s.err
}

func TestReadBDPrefix_NilStore(t *testing.T) {
	if got := readBDPrefix(context.Background(), nil); got != "" {
		t.Errorf("readBDPrefix(nil) = %q; want empty", got)
	}
}

func TestReadBDPrefix_ConfigError(t *testing.T) {
	s := &stubConfigStore{err: errors.New("not initialized")}
	if got := readBDPrefix(context.Background(), s); got != "" {
		t.Errorf("readBDPrefix on error = %q; want empty (so bd.prefix attribute is omitted)", got)
	}
}

func TestReadBDPrefix_TrimsTrailingDash(t *testing.T) {
	cases := map[string]string{
		"myproj-": "myproj",
		"myproj":  "myproj",
		"":        "",
		"a-b-":    "a-b",
	}
	for in, want := range cases {
		s := &stubConfigStore{value: in}
		if got := readBDPrefix(context.Background(), s); got != want {
			t.Errorf("readBDPrefix(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestCommandSpanAttrs(t *testing.T) {
	got := commandSpanAttrs("ready", "1.0.4-dev", "alice", []string{"--json", "--include-deferred"})

	want := map[string]string{
		"bd.command": "ready",
		"bd.version": "1.0.4-dev",
		"bd.args":    "--json --include-deferred",
		"bd.actor":   "alice",
	}
	if len(got) != len(want) {
		t.Fatalf("commandSpanAttrs returned %d attrs; want %d", len(got), len(want))
	}
	for _, kv := range got {
		expected, ok := want[string(kv.Key)]
		if !ok {
			t.Errorf("unexpected attribute %q in span attrs", kv.Key)
			continue
		}
		if kv.Value.AsString() != expected {
			t.Errorf("attr %q = %q; want %q", kv.Key, kv.Value.AsString(), expected)
		}
	}
}

func TestCommandSpanAttrs_EmptyArgs(t *testing.T) {
	got := commandSpanAttrs("status", "v1", "bot", nil)
	for _, kv := range got {
		if string(kv.Key) == "bd.args" && kv.Value.AsString() != "" {
			t.Errorf("bd.args with nil args = %q; want empty string", kv.Value.AsString())
		}
	}
}

// startCommandTelemetry composes Init + Tracer.Start. The bd.command.<name>
// span is the parent for every storage and AI span downstream — silently
// returning nil here would break trace nesting across the whole invocation.
// Whether the span actually records is governed by telemetry.Init (covered
// in internal/telemetry/telemetry_test.go); this test guards the wiring
// contract: the function must always return a non-nil context and span.
func TestStartCommandTelemetry_DisabledStillReturnsSpan(t *testing.T) {
	clearTelemetryEnv(t)
	ctx, span := startCommandTelemetry(context.Background(), nil, "ready", "1.0.0", "alice", []string{"--json"})
	if ctx == nil {
		t.Fatal("startCommandTelemetry returned nil context")
	}
	if span == nil {
		t.Fatal("startCommandTelemetry returned nil span")
	}
	span.End()
}

// startCommandTelemetry must read the issue prefix from the store before
// calling Init so the bd.prefix resource attribute is stamped. Asserting
// directly on the resource requires reaching into telemetry global state
// across tests; instead we verify the upstream piece: a store that returns
// a prefix is consulted via readBDPrefix without panic, and the returned
// span chain is well-formed.
func TestStartCommandTelemetry_PrefixedStore(t *testing.T) {
	clearTelemetryEnv(t)
	store := &stubConfigStore{value: "myproj-"}
	ctx, span := startCommandTelemetry(context.Background(), store, "ready", "1.0.0", "alice", []string{"--json"})
	if ctx == nil || span == nil {
		t.Fatalf("startCommandTelemetry(prefixed store) returned ctx=%v span=%v; want both non-nil", ctx, span)
	}
	span.End()
}
