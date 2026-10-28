//go:build cgo

package doltinfoenv

import (
	"testing"

	"github.com/dolthub/dolt/go/store/blobstore"
)

// TestEnvVarMatchesDolt keeps the local pin honest: EnvVar must stay Dolt's
// own InfoBranchEnvVar, and Dolt must keep resolving a set-to-empty value as
// "do not publish". A go.mod bump that renamed either would otherwise make
// every guard in this package a silent no-op and republish the info branch.
func TestEnvVarMatchesDolt(t *testing.T) {
	if EnvVar != blobstore.InfoBranchEnvVar {
		t.Errorf("EnvVar = %q, want Dolt's %q", EnvVar, blobstore.InfoBranchEnvVar)
	}
	t.Setenv(EnvVar, "")
	if got := blobstore.ResolveInfoBranch(blobstore.DefaultInfoBranch); got != "" {
		t.Errorf("ResolveInfoBranch(%q) with %s set to empty = %q, want empty (disabled)",
			blobstore.DefaultInfoBranch, EnvVar, got)
	}
}
