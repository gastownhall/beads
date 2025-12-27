package routing

import (
	"testing"
)

func TestDetermineTargetRepo(t *testing.T) {
	tests := []struct {
		name     string
		config   *RoutingConfig
		userRole UserRole
		repoPath string
		want     string
	}{
		{
			name: "explicit override takes precedence",
			config: &RoutingConfig{
				Mode:                "auto",
				DefaultRepo:         "~/planning",
				MaintainerRepo:      ".",
				ContributorRepo:     "~/contributor-planning",
				ExplicitOverride:    "/tmp/custom",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     "/tmp/custom",
		},
		{
			name: "auto mode - maintainer uses maintainer repo",
			config: &RoutingConfig{
				Mode:            "auto",
				MaintainerRepo:  ".",
				ContributorRepo: "~/contributor-planning",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     ".",
		},
		{
			name: "auto mode - contributor uses contributor repo",
			config: &RoutingConfig{
				Mode:            "auto",
				MaintainerRepo:  ".",
				ContributorRepo: "~/contributor-planning",
			},
			userRole: Contributor,
			repoPath: ".",
			want:     "~/contributor-planning",
		},
		{
			name: "explicit mode uses default",
			config: &RoutingConfig{
				Mode:        "explicit",
				DefaultRepo: "~/planning",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     "~/planning",
		},
		{
			name: "no config defaults to current directory",
			config: &RoutingConfig{
				Mode: "auto",
			},
			userRole: Maintainer,
			repoPath: ".",
			want:     ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineTargetRepo(tt.config, tt.userRole, tt.repoPath)
			if got != tt.want {
				t.Errorf("DetermineTargetRepo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectUserRole_Fallback(t *testing.T) {
	// Test fallback behavior when git is not available
	role, err := DetectUserRole("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("DetectUserRole() error = %v, want nil", err)
	}
	if role != Contributor {
		t.Errorf("DetectUserRole() = %v, want %v (fallback)", role, Contributor)
	}
}
