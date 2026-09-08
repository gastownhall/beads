//go:build windows

package config

import (
	"path/filepath"
	"testing"
)

func TestWindowsUserConfigCandidatesRejectMSYSRoots(t *testing.T) {
	candidates := buildUserConfigYamlCandidates("/c/Users/tester", nil, "/c/Users/tester/AppData/Roaming", nil)
	if candidates.legacy != "" || candidates.documented != "" || candidates.native != "" {
		t.Fatalf("MSYS roots produced native filesystem candidates: %#v", candidates)
	}
	if _, err := selectUserConfigYamlPath(candidates); err == nil {
		t.Fatal("selectUserConfigYamlPath returned nil error for MSYS roots")
	}
}

func TestWindowsUserConfigCandidatesAcceptUNCPaths(t *testing.T) {
	home := `\\server\profiles\tester`
	native := `\\server\profiles\tester\AppData\Roaming`
	candidates := buildUserConfigYamlCandidates(home, nil, native, nil)

	for name, path := range map[string]string{
		"legacy":     candidates.legacy,
		"native":     candidates.native,
		"documented": candidates.documented,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s UNC path %q is not absolute", name, path)
		}
	}
	if candidates.documented != filepath.Join(home, ".config", "bd", "config.yaml") {
		t.Fatalf("documented UNC path = %q", candidates.documented)
	}
}
