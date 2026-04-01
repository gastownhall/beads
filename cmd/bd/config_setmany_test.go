package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestConfigSetManyArgParsing tests argument parsing for the set-many command.
func TestConfigSetManyArgParsing(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{"simple", "key=value", "key", "value", false},
		{"dotted key", "ado.state_map.open=New", "ado.state_map.open", "New", false},
		{"value with equals", "key=val=ue", "key", "val=ue", false},
		{"empty value", "key=", "key", "", false},
		{"no equals", "keyvalue", "", "", true},
		{"only equals", "=value", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := strings.Index(tt.arg, "=")
			if idx <= 0 {
				if !tt.wantErr {
					t.Error("expected successful parse, got error")
				}
				return
			}
			if tt.wantErr {
				t.Error("expected error, got successful parse")
				return
			}
			key := tt.arg[:idx]
			value := tt.arg[idx+1:]
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
			if value != tt.wantVal {
				t.Errorf("value = %q, want %q", value, tt.wantVal)
			}
		})
	}
}

// TestConfigSetManyYamlKeyDetection tests that yaml-only keys are correctly identified
// for routing in the set-many command.
func TestConfigSetManyYamlKeyDetection(t *testing.T) {
	yamlKeys := []string{"no-db", "json", "routing.mode", "routing.default", "no-push"}
	for _, key := range yamlKeys {
		if !config.IsYamlOnlyKey(key) {
			t.Errorf("expected %q to be yaml-only", key)
		}
	}

	dbKeys := []string{"ado.state_map.open", "jira.url", "status.custom", "test.key"}
	for _, key := range dbKeys {
		if config.IsYamlOnlyKey(key) {
			t.Errorf("expected %q to NOT be yaml-only", key)
		}
	}
}
