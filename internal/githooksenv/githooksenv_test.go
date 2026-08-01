package githooksenv

import (
	"os"
	"testing"
)

func TestAppendParameter(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{"absent", "", NoHooksParam},
		{"preserved and appended so ours wins", "'user.email=ci@example.com'", "'user.email=ci@example.com' " + NoHooksParam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppendParameter(tt.existing, NoHooksParam); got != tt.want {
				t.Errorf("AppendParameter(%q) = %q, want %q", tt.existing, got, tt.want)
			}
		})
	}
}

// TestExtract pins the last-wins rule, matching how exec resolves a duplicated
// key in an environment slice.
func TestExtract(t *testing.T) {
	env := []string{"PATH=/bin", ParametersEnv + "='a=1'", "TZ=UTC", ParametersEnv + "='b=2'"}
	if got, want := Extract(env), "'b=2'"; got != want {
		t.Errorf("Extract = %q, want %q", got, want)
	}
	if got := Extract([]string{"PATH=/bin"}); got != "" {
		t.Errorf("Extract with no entry = %q, want empty", got)
	}
}

func TestWithDisabled_SetsAndRestoresWhenUnset(t *testing.T) {
	if err := os.Unsetenv(ParametersEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(ParametersEnv) })

	var inside string
	err := WithDisabled(func() error {
		inside = os.Getenv(ParametersEnv)
		return nil
	})
	if err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if inside != NoHooksParam {
		t.Errorf("inside fn %s = %q, want %q", ParametersEnv, inside, NoHooksParam)
	}
	if _, ok := os.LookupEnv(ParametersEnv); ok {
		t.Errorf("%s still set after WithDisabled; want unset again", ParametersEnv)
	}
}

func TestWithDisabled_RestoresPreviousValue(t *testing.T) {
	const prev = "'user.email=ci@example.com'"
	t.Setenv(ParametersEnv, prev)

	var inside string
	if err := WithDisabled(func() error {
		inside = os.Getenv(ParametersEnv)
		return nil
	}); err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if want := prev + " " + NoHooksParam; inside != want {
		t.Errorf("inside fn %s = %q, want %q", ParametersEnv, inside, want)
	}
	if got := os.Getenv(ParametersEnv); got != prev {
		t.Errorf("after WithDisabled %s = %q, want the original %q", ParametersEnv, got, prev)
	}
}

// TestWithDisabled_Nested guards the refcount: the server-mode credential path
// and the shared versioncontrolops path can both wrap the same call, and the
// inner scope must not restore the variable out from under the outer one.
func TestWithDisabled_Nested(t *testing.T) {
	if err := os.Unsetenv(ParametersEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(ParametersEnv) })

	var afterInner string
	err := WithDisabled(func() error {
		if err := WithDisabled(func() error { return nil }); err != nil {
			return err
		}
		afterInner = os.Getenv(ParametersEnv)
		return nil
	})
	if err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if afterInner != NoHooksParam {
		t.Errorf("after inner scope exited, %s = %q, want still %q", ParametersEnv, afterInner, NoHooksParam)
	}
	if _, ok := os.LookupEnv(ParametersEnv); ok {
		t.Errorf("%s still set after the outer scope; want unset again", ParametersEnv)
	}
}

// TestWithDisabled_NoDoubleAppendWhenNested checks the inner scope reuses the
// outer activation rather than stacking a second copy of the parameter, which
// git would apply twice.
func TestWithDisabled_NoDoubleAppendWhenNested(t *testing.T) {
	if err := os.Unsetenv(ParametersEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(ParametersEnv) })

	var innermost string
	if err := WithDisabled(func() error {
		return WithDisabled(func() error {
			innermost = os.Getenv(ParametersEnv)
			return nil
		})
	}); err != nil {
		t.Fatalf("WithDisabled: %v", err)
	}
	if innermost != NoHooksParam {
		t.Errorf("innermost %s = %q, want a single %q", ParametersEnv, innermost, NoHooksParam)
	}
}
