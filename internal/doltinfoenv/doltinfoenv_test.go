package doltinfoenv

import (
	"errors"
	"os"
	"slices"
	"testing"
)

func TestDisabledEnvAddsEmptyWhenAbsent(t *testing.T) {
	in := []string{"PATH=/usr/bin", "DOLT_USER=x"}
	got := DisabledEnv(in)
	want := []string{"PATH=/usr/bin", "DOLT_USER=x", EnvVar + "="}
	if !slices.Equal(got, want) {
		t.Errorf("DisabledEnv(%q) = %q, want %q", in, got, want)
	}
	if len(in) != 2 {
		t.Errorf("DisabledEnv mutated its input: %q", in)
	}
}

func TestDisabledEnvPreservesUserValue(t *testing.T) {
	tests := []struct {
		name string
		in   []string
	}{
		{
			name: "a name the user opted in with",
			in:   []string{"PATH=/usr/bin", EnvVar + "=dolt-info"},
		},
		{
			name: "empty, Dolt's own opt-out form",
			in:   []string{"PATH=/usr/bin", EnvVar + "="},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DisabledEnv(tt.in)
			if !slices.Equal(got, tt.in) {
				t.Errorf("DisabledEnv(%q) = %q, want the input unchanged", tt.in, got)
			}
		})
	}
}

func TestWithDisabled_SetsAndRestoresWhenAbsent(t *testing.T) {
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(EnvVar) })

	var inside string
	var insideOK bool
	if err := WithDisabled(func() error {
		inside, insideOK = os.LookupEnv(EnvVar)
		return nil
	}); err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	// Set to empty, not merely present: Dolt resolves set-to-empty as
	// "do not publish", the whole point of the guard.
	if !insideOK || inside != "" {
		t.Errorf("inside fn %s = (%q, %v); want set to empty", EnvVar, inside, insideOK)
	}
	if _, ok := os.LookupEnv(EnvVar); ok {
		t.Errorf("%s still set after WithDisabled; want unset again", EnvVar)
	}
}

func TestWithDisabled_KeepsUserValue(t *testing.T) {
	t.Setenv(EnvVar, "dolt-info")
	if err := WithDisabled(func() error {
		if got := os.Getenv(EnvVar); got != "dolt-info" {
			t.Errorf("inside fn %s = %q, want the user's %q", EnvVar, got, "dolt-info")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if got := os.Getenv(EnvVar); got != "dolt-info" {
		t.Errorf("after WithDisabled %s = %q, want the user's %q", EnvVar, got, "dolt-info")
	}
}

func TestWithDisabled_KeepsUserEmptyValue(t *testing.T) {
	// Set-to-empty is a choice the user made (the opt-out); it must stay
	// distinct from unset so the release path can tell them apart.
	t.Setenv(EnvVar, "")
	if err := WithDisabled(func() error {
		if got, ok := os.LookupEnv(EnvVar); !ok || got != "" {
			t.Errorf("inside fn %s = (%q, %v); want set to empty", EnvVar, got, ok)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if got, ok := os.LookupEnv(EnvVar); !ok || got != "" {
		t.Errorf("after WithDisabled %s = (%q, %v); want still set to empty", EnvVar, got, ok)
	}
}

// TestWithDisabled_Nested guards the refcount: the server-mode credential
// path and the shared versioncontrolops path can both wrap the same call,
// and the inner scope must not restore the variable out from under the
// outer one.
func TestWithDisabled_Nested(t *testing.T) {
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(EnvVar) })

	var afterInner string
	err := WithDisabled(func() error {
		if err := WithDisabled(func() error { return nil }); err != nil {
			return err
		}
		afterInner = os.Getenv(EnvVar)
		return nil
	})
	if err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if afterInner != "" {
		t.Errorf("after inner scope exited, %s = %q, want still empty", EnvVar, afterInner)
	}
	if _, ok := os.LookupEnv(EnvVar); ok {
		t.Errorf("%s still set after the outer scope; want unset again", EnvVar)
	}
}

func TestWithDisabled_ReturnsFnError(t *testing.T) {
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(EnvVar) })

	sentinel := errors.New("transfer failed")
	if err := WithDisabled(func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("WithDisabled returned %v, want %v", err, sentinel)
	}
	if _, ok := os.LookupEnv(EnvVar); ok {
		t.Errorf("%s still set after WithDisabled failed; want unset again", EnvVar)
	}
}
