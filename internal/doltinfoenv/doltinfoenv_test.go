package doltinfoenv

import (
	"slices"
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
