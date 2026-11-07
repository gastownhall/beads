package doltinfoenv

import (
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/dolthub/dolt/go/store/blobstore"
)

// TestEnvVarMatchesDolt keeps the pinned name and the "empty disables"
// contract in sync with Dolt's git blobstore (store/blobstore/git_refs.go):
// a Dolt bump that renames the variable or changes what empty means fails
// here instead of silently publishing __dolt_remote_info__ again.
func TestEnvVarMatchesDolt(t *testing.T) {
	if EnvVar != blobstore.InfoBranchEnvVar {
		t.Fatalf("EnvVar = %q, dolt reads %q", EnvVar, blobstore.InfoBranchEnvVar)
	}
	t.Setenv(EnvVar, "")
	if got := blobstore.ResolveInfoBranch(blobstore.DefaultInfoBranch); got != "" {
		t.Errorf("with %s empty, dolt resolves the info branch to %q, want disabled (\"\")", EnvVar, got)
	}
}

func TestDisabledEnv(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "absent: adds one empty entry",
			in:   []string{"PATH=/usr/bin", "HOME=/home/u"},
			want: []string{"PATH=/usr/bin", "HOME=/home/u", EnvVar + "="},
		},
		{
			name: "user's branch name is kept",
			in:   []string{"PATH=/usr/bin", EnvVar + "=dolt-info"},
			want: []string{"PATH=/usr/bin", EnvVar + "=dolt-info"},
		},
		{
			name: "user's empty value is kept",
			in:   []string{EnvVar + "=", "PATH=/usr/bin"},
			want: []string{EnvVar + "=", "PATH=/usr/bin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := slices.Clone(tt.in)
			if got := DisabledEnv(in); !slices.Equal(got, tt.want) {
				t.Errorf("DisabledEnv(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !slices.Equal(in, tt.in) {
				t.Errorf("DisabledEnv modified its input: %q, want %q", in, tt.in)
			}
		})
	}
}

// unsetForTest unsets EnvVar for the test and restores it afterwards.
func unsetForTest(t *testing.T) {
	t.Helper()
	t.Setenv(EnvVar, "")
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
}

func lookup() string {
	if v, ok := os.LookupEnv(EnvVar); ok {
		return strconv.Quote(v)
	}
	return "unset"
}

func TestWithDisabled(t *testing.T) {
	t.Run("absent: empty during fn, unset after", func(t *testing.T) {
		unsetForTest(t)
		var during string
		if err := WithDisabled(func() error { during = lookup(); return nil }); err != nil {
			t.Fatal(err)
		}
		if during != `""` {
			t.Errorf("during fn, %s = %s, want \"\"", EnvVar, during)
		}
		if got := lookup(); got != "unset" {
			t.Errorf("after fn, %s = %s, want unset", EnvVar, got)
		}
	})

	for _, value := range []string{"dolt-info", ""} {
		t.Run("user value "+strconv.Quote(value)+" is kept", func(t *testing.T) {
			t.Setenv(EnvVar, value)
			want := strconv.Quote(value)
			var during string
			if err := WithDisabled(func() error { during = lookup(); return nil }); err != nil {
				t.Fatal(err)
			}
			if during != want || lookup() != want {
				t.Errorf("%s during fn = %s, after = %s, want %s both times", EnvVar, during, lookup(), want)
			}
		})
	}

	t.Run("nested: unset only when the outermost call returns", func(t *testing.T) {
		unsetForTest(t)
		var afterInner string
		_ = WithDisabled(func() error {
			_ = WithDisabled(func() error { return nil })
			afterInner = lookup()
			return nil
		})
		if afterInner != `""` {
			t.Errorf("after the inner call, %s = %s, want \"\" until the outer call returns", EnvVar, afterInner)
		}
		if got := lookup(); got != "unset" {
			t.Errorf("after the outer call, %s = %s, want unset", EnvVar, got)
		}
	})

	// Two goroutines' remote calls overlap: A starts first and returns while
	// B is still running. B must keep the empty value until it returns too.
	t.Run("overlapping calls", func(t *testing.T) {
		unsetForTest(t)
		bStarted, aDone := make(chan struct{}), make(chan struct{})
		bSaw := make(chan string, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		_ = WithDisabled(func() error {
			go func() {
				defer wg.Done()
				_ = WithDisabled(func() error {
					close(bStarted)
					<-aDone
					bSaw <- lookup()
					return nil
				})
			}()
			<-bStarted
			return nil
		})
		close(aDone)
		wg.Wait()
		if got := <-bSaw; got != `""` {
			t.Errorf("in B after A returned, %s = %s, want \"\"", EnvVar, got)
		}
		if got := lookup(); got != "unset" {
			t.Errorf("after both calls, %s = %s, want unset", EnvVar, got)
		}
	})
}
