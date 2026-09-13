package config

import "testing"

func TestValidateGitDataRef(t *testing.T) {
	valid := []string{"", "  ", "refs/dolt/data", "refs/heads/issue-data", "refs/dolt/units/team-12542", "refs/heads/feature/with.dots"}
	for _, v := range valid {
		if err := ValidateGitDataRef(v); err != nil {
			t.Errorf("ValidateGitDataRef(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{
		"issue-data", "ref/heads/x", "heads/x", "-refs/heads/x",
		"refs/heads/*", "refs/heads/a?b", "refs/heads/a[b]", "refs/heads/a..b",
		"refs/heads/a.lock", "refs/heads/.hidden", "refs/heads/a/", "refs/heads/a.",
		"refs/heads/a@{1}", "refs/heads/a//b", "refs/heads/a~1", "refs/heads/a^2",
		"refs/heads/a:b", "refs/heads/a\\b", "refs/heads/has space", "refs/heads/tab\tx",
		"refs/heads/" + DefaultDoltRemoteInfoBranch,
	}
	for _, v := range invalid {
		if err := ValidateGitDataRef(v); err == nil {
			t.Errorf("ValidateGitDataRef(%q) = nil, want error", v)
		}
	}
}

// The info branch dolt overwrites after a push follows DOLT_REMOTE_INFO_BRANCH:
// the overridden name is refused as a data ref, the default becomes usable,
// and an empty override (marker disabled) refuses nothing.
func TestValidateGitDataRefFollowsInfoBranchOverride(t *testing.T) {
	t.Setenv(DoltRemoteInfoBranchEnvVar, "issue-data")
	if got := DoltRemoteInfoRef(); got != "refs/heads/issue-data" {
		t.Fatalf("DoltRemoteInfoRef = %q", got)
	}
	if err := ValidateGitDataRef("refs/heads/issue-data"); err == nil {
		t.Error("the overridden info branch must be refused as a data ref")
	}
	if err := ValidateGitDataRef("refs/heads/" + DefaultDoltRemoteInfoBranch); err != nil {
		t.Errorf("the default name is free once overridden: %v", err)
	}

	// Dolt prefixes refs/heads/ unconditionally, so a refs/-shaped override
	// reserves the doubled ref dolt writes, not the ref the value names.
	t.Setenv(DoltRemoteInfoBranchEnvVar, "refs/heads/marker")
	if got := DoltRemoteInfoRef(); got != "refs/heads/refs/heads/marker" {
		t.Fatalf("refs/-shaped override: DoltRemoteInfoRef = %q", got)
	}
	if err := ValidateGitDataRef("refs/heads/marker"); err != nil {
		t.Errorf("refs/heads/marker is not what dolt overwrites: %v", err)
	}
	if err := ValidateGitDataRef("refs/heads/refs/heads/marker"); err == nil {
		t.Error("refs/heads/refs/heads/marker is dolt's info branch here and must be refused")
	}

	t.Setenv(DoltRemoteInfoBranchEnvVar, "")
	if got := DoltRemoteInfoRef(); got != "" {
		t.Fatalf("disabled marker: DoltRemoteInfoRef = %q, want empty", got)
	}
	if err := ValidateGitDataRef("refs/heads/" + DefaultDoltRemoteInfoBranch); err != nil {
		t.Errorf("with the marker disabled nothing is reserved: %v", err)
	}
}

// bd config set applies the same rules as --ref, so a committed value cannot
// carry a shape the flags refuse.
func TestValidateYamlConfigValueSyncRemoteRef(t *testing.T) {
	if err := validateYamlConfigValue("sync.remote-ref", "refs/dolt/units/team-12542"); err != nil {
		t.Fatalf("valid ref rejected: %v", err)
	}
	if err := validateYamlConfigValue("sync.remote-ref", ""); err != nil {
		t.Fatalf("empty (default) rejected: %v", err)
	}
	for _, v := range []string{"issue-data", "refs/heads/*", "refs/heads/" + DefaultDoltRemoteInfoBranch} {
		if err := validateYamlConfigValue("sync.remote-ref", v); err == nil {
			t.Errorf("validateYamlConfigValue(sync.remote-ref, %q) = nil, want error", v)
		}
	}
}
