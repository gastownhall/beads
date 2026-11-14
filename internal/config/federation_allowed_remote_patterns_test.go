package config

import (
	"reflect"
	"testing"
)

// TestAllowedRemotePatternsEmptyDefault pins AC-1 of GH#6233: with no
// configuration, the allowlist is empty, and an empty list means the setting
// is off — every enforcement gate reading it becomes a no-op.
func TestAllowedRemotePatternsEmptyDefault(t *testing.T) {
	defer envSnapshot(t)()
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := AllowedRemotePatterns(); len(got) != 0 {
		t.Errorf("AllowedRemotePatterns() = %v with no configuration, want empty", got)
	}
	if got := GetFederationConfig().AllowedRemotePatterns; len(got) != 0 {
		t.Errorf("GetFederationConfig().AllowedRemotePatterns = %v with no configuration, want empty", got)
	}
}

// TestAllowedRemotePatternsPreservesBlankEntries pins the blank-entry rule:
// a non-empty list is deny-by-default, so blank entries must survive the
// read. Dropping them would turn a deny-all list into an allow-all one.
func TestAllowedRemotePatternsPreservesBlankEntries(t *testing.T) {
	defer envSnapshot(t)()
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	Set("federation.allowed-remote-patterns", []string{"", "git+https://github.com/myorg/*", ""})
	got := AllowedRemotePatterns()
	want := []string{"", "git+https://github.com/myorg/*", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedRemotePatterns() = %#v, want %#v (blank entries preserved)", got, want)
	}
	if got := GetFederationConfig().AllowedRemotePatterns; !reflect.DeepEqual(got, want) {
		t.Errorf("GetFederationConfig().AllowedRemotePatterns = %#v, want %#v", got, want)
	}
}
