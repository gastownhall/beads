package creds

import (
	"context"
	"errors"
	"testing"
)

// stubSource is a Source with a fixed answer, for ladder tests.
type stubSource struct {
	name       string
	cred       Credential
	configured bool
	err        error
}

func (s stubSource) Name() string { return s.name }
func (s stubSource) Resolve(_ context.Context) (Credential, bool, error) {
	return s.cred, s.configured, s.err
}

func TestResolveLadderFirstConfiguredWins(t *testing.T) {
	high := stubSource{name: "high", cred: Credential{Value: "from-high", Kind: KindSecret}, configured: true}
	low := stubSource{name: "low", cred: Credential{Value: "from-low", Kind: KindSecret}, configured: true}

	got, ok, err := ResolveLadder(context.Background(), high, low)
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.Value != "from-high" {
		t.Fatalf("value = %q, want from-high (higher rung must win)", got.Value)
	}
	if got.Source != "high" {
		t.Fatalf("source = %q, want high", got.Source)
	}
}

func TestResolveLadderSkipsUnconfigured(t *testing.T) {
	empty := stubSource{name: "empty", configured: false}
	real := stubSource{name: "real", cred: Credential{Value: "pw"}, configured: true}

	got, ok, err := ResolveLadder(context.Background(), empty, real)
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.Value != "pw" {
		t.Fatalf("value = %q, want pw", got.Value)
	}
}

// Fail-closed: a configured-but-erroring source aborts the walk and never falls
// through to a lower rung, even a valid one.
func TestResolveLadderFailsClosed(t *testing.T) {
	sentinel := errors.New("helper exploded")
	broken := stubSource{name: "cmd", configured: true, err: sentinel}
	fallback := stubSource{name: "env", cred: Credential{Value: "would-be-wrong"}, configured: true}

	_, ok, err := ResolveLadder(context.Background(), broken, fallback)
	if err == nil {
		t.Fatal("expected an error from the erroring source, got nil (fell through — NOT fail-closed)")
	}
	if !ok {
		t.Fatal("expected configured=true on error (a configured source errored)")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v does not wrap the source error", err)
	}
}

func TestResolveLadderNothingConfigured(t *testing.T) {
	a := stubSource{name: "a", configured: false}
	b := stubSource{name: "b", configured: false}

	_, ok, err := ResolveLadder(context.Background(), a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected configured=false when nothing is set (driver-native fallthrough)")
	}
}

func TestEnvSource(t *testing.T) {
	t.Setenv("BEADS_CREDS_TEST_VAR", "hunter2")
	got, ok, err := EnvSource{Var: "BEADS_CREDS_TEST_VAR"}.Resolve(context.Background())
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if got.Value != "hunter2" || got.Kind != KindSecret || got.Source != "BEADS_CREDS_TEST_VAR" {
		t.Fatalf("unexpected credential: %+v", got)
	}

	t.Setenv("BEADS_CREDS_TEST_VAR", "")
	_, ok, err = EnvSource{Var: "BEADS_CREDS_TEST_VAR"}.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected configured=false for an empty env var")
	}
}
