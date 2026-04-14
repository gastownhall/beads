package handoffauth

import (
	"context"
	"fmt"
	"testing"
)

// mockConfig implements ConfigGetter for testing.
type mockConfig struct {
	values map[string]string
}

func (m *mockConfig) GetConfig(_ context.Context, key string) (string, error) {
	if v, ok := m.values[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("key not found")
}

func TestCheckSendAllowed(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		target  string
		wantErr bool
	}{
		{"wildcard allows all", "*", "any-project", false},
		{"explicit match", "frontend,backend", "frontend", false},
		{"explicit match second", "frontend,backend", "backend", false},
		{"not in list", "frontend,backend", "other", true},
		{"empty policy denies", "", "any", true},
		{"single project match", "frontend", "frontend", true}, // should work
		{"whitespace trimmed", " frontend , backend ", "frontend", false},
	}
	// Fix: single project match should pass
	tests[5] = struct {
		name    string
		policy  string
		target  string
		wantErr bool
	}{"single project match", "frontend", "frontend", false}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &mockConfig{values: map[string]string{}}
			if tt.policy != "" {
				cfg.values["handoff.allow_send_to"] = tt.policy
			}
			err := CheckSendAllowed(context.Background(), cfg, tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckSendAllowed() error = %v, wantErr %v", err, tt.wantErr)
			}
			// Verify error messages don't leak project names for denied sends
			if err != nil && tt.policy != "" {
				if contains(err.Error(), tt.target) {
					t.Errorf("error message leaks target name: %v", err)
				}
			}
		})
	}
}

func TestCheckAcceptAllowed(t *testing.T) {
	tests := []struct {
		name    string
		policy  string // empty means no policy configured
		sender  string
		wantErr bool
	}{
		{"no policy accepts all", "", "any-project", false},
		{"wildcard accepts all", "*", "any-project", false},
		{"explicit match", "project-a,project-b", "project-a", false},
		{"not in list", "project-a,project-b", "project-c", true},
		{"single sender match", "project-a", "project-a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &mockConfig{values: map[string]string{}}
			if tt.policy != "" {
				cfg.values["handoff.accept_from"] = tt.policy
			}
			err := CheckAcceptAllowed(context.Background(), cfg, tt.sender)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckAcceptAllowed() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMatchesPolicy(t *testing.T) {
	tests := []struct {
		policy  string
		project string
		want    bool
	}{
		{"*", "anything", true},
		{"a,b,c", "b", true},
		{"a,b,c", "d", false},
		{" a , b , c ", "b", true},
		{"single", "single", true},
		{"single", "other", false},
	}
	for _, tt := range tests {
		got := matchesPolicy(tt.policy, tt.project)
		if got != tt.want {
			t.Errorf("matchesPolicy(%q, %q) = %v, want %v", tt.policy, tt.project, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
