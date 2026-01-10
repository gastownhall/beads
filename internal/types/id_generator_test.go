package types

import (
	"testing"
	"time"
)

func TestGenerateHashID(t *testing.T) {
	now := time.Date(2025, 10, 30, 12, 0, 0, 0, time.UTC)
	
	tests := []struct {
		name        string
		prefix      string
		title       string
		description string
		created     time.Time
		workspaceID string
		wantLen     int
	}{
		{
			name:        "basic hash ID",
			prefix:      "bd",
			title:       "Fix auth bug",
			description: "Users can't log in",
			created:     now,
			workspaceID: "workspace-1",
			wantLen:     64, // Full SHA256 hex
		},
		{
			name:        "different prefix ignored (returns hash only)",
			prefix:      "ticket",
			title:       "Fix auth bug",
			description: "Users can't log in",
			created:     now,
			workspaceID: "workspace-1",
			wantLen:     64, // Full SHA256 hex
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := GenerateHashID(tt.prefix, tt.title, tt.description, tt.created, tt.workspaceID)
			
			// Check length (full SHA256 = 64 hex chars)
			if len(hash) != tt.wantLen {
				t.Errorf("expected length %d, got %d", tt.wantLen, len(hash))
			}
			
			// Check all hex characters
			for _, ch := range hash {
				if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
					t.Errorf("non-hex character in hash: %c", ch)
				}
			}
		})
	}
}

func TestGenerateHashID_Deterministic(t *testing.T) {
	now := time.Date(2025, 10, 30, 12, 0, 0, 0, time.UTC)
	
	// Same inputs should produce same hash
	hash1 := GenerateHashID("bd", "Title", "Desc", now, "ws1")
	hash2 := GenerateHashID("bd", "Title", "Desc", now, "ws1")
	
	if hash1 != hash2 {
		t.Errorf("expected deterministic hash, got %s and %s", hash1, hash2)
	}
}

func TestGenerateHashID_DifferentInputs(t *testing.T) {
	now := time.Date(2025, 10, 30, 12, 0, 0, 0, time.UTC)
	
	baseHash := GenerateHashID("bd", "Title", "Desc", now, "ws1")
	
	tests := []struct {
		name        string
		title       string
		description string
		created     time.Time
		workspaceID string
	}{
		{"different title", "Other", "Desc", now, "ws1"},
		{"different description", "Title", "Other", now, "ws1"},
		{"different timestamp", "Title", "Desc", now.Add(time.Nanosecond), "ws1"},
		{"different workspace", "Title", "Desc", now, "ws2"},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := GenerateHashID("bd", tt.title, tt.description, tt.created, tt.workspaceID)
			if hash == baseHash {
				t.Errorf("expected different hash for %s, got same: %s", tt.name, hash)
			}
		})
	}
}

func TestGenerateChildID(t *testing.T) {
	tests := []struct {
		name        string
		parentID    string
		childNumber int
		want        string
	}{
		{
			name:        "first level child",
			parentID:    "bd-af78e9a2",
			childNumber: 1,
			want:        "bd-af78e9a2.1",
		},
		{
			name:        "second level child",
			parentID:    "bd-af78e9a2.1",
			childNumber: 2,
			want:        "bd-af78e9a2.1.2",
		},
		{
			name:        "third level child",
			parentID:    "bd-af78e9a2.1.2",
			childNumber: 3,
			want:        "bd-af78e9a2.1.2.3",
		},
		{
			name:        "large child number",
			parentID:    "bd-af78e9a2",
			childNumber: 347,
			want:        "bd-af78e9a2.347",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateChildID(tt.parentID, tt.childNumber)
			if got != tt.want {
				t.Errorf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestParseHierarchicalID(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		wantRoot   string
		wantParent string
		wantDepth  int
	}{
		{
			name:       "root level (no parent)",
			id:         "bd-af78e9a2",
			wantRoot:   "bd-af78e9a2",
			wantParent: "",
			wantDepth:  0,
		},
		{
			name:       "first level child",
			id:         "bd-af78e9a2.1",
			wantRoot:   "bd-af78e9a2",
			wantParent: "bd-af78e9a2",
			wantDepth:  1,
		},
		{
			name:       "second level child",
			id:         "bd-af78e9a2.1.2",
			wantRoot:   "bd-af78e9a2",
			wantParent: "bd-af78e9a2.1",
			wantDepth:  2,
		},
		{
			name:       "third level child",
			id:         "bd-af78e9a2.1.2.3",
			wantRoot:   "bd-af78e9a2",
			wantParent: "bd-af78e9a2.1.2",
			wantDepth:  3,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRoot, gotParent, gotDepth := ParseHierarchicalID(tt.id)
			
			if gotRoot != tt.wantRoot {
				t.Errorf("root: expected %s, got %s", tt.wantRoot, gotRoot)
			}
			if gotParent != tt.wantParent {
				t.Errorf("parent: expected %s, got %s", tt.wantParent, gotParent)
			}
			if gotDepth != tt.wantDepth {
				t.Errorf("depth: expected %d, got %d", tt.wantDepth, gotDepth)
			}
		})
	}
}

func BenchmarkGenerateHashID(b *testing.B) {
	now := time.Now()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GenerateHashID("bd", "Fix auth bug", "Users can't log in", now, "workspace-1")
	}
}

func BenchmarkGenerateChildID(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateChildID("bd-af78e9a2", 42)
	}
}

func TestCheckHierarchyDepth(t *testing.T) {
	tests := []struct {
		name     string
		parentID string
		maxDepth int
		wantErr  bool
		errMsg   string
	}{
		// Default maxDepth (uses MaxHierarchyDepth = 3)
		{"root parent with default depth", "bd-abc123", 0, false, ""},
		{"depth 1 parent with default depth", "bd-abc123.1", 0, false, ""},
		{"depth 2 parent with default depth", "bd-abc123.1.2", 0, false, ""},
		{"depth 3 parent with default depth - exceeds", "bd-abc123.1.2.3", 0, true, "maximum hierarchy depth (3) exceeded for parent bd-abc123.1.2.3"},

		// Custom maxDepth
		{"root parent with max=1", "bd-abc123", 1, false, ""},
		{"depth 1 parent with max=1 - exceeds", "bd-abc123.1", 1, true, "maximum hierarchy depth (1) exceeded for parent bd-abc123.1"},
		{"depth 3 parent with max=5", "bd-abc123.1.2.3", 5, false, ""},
		{"depth 4 parent with max=5", "bd-abc123.1.2.3.4", 5, false, ""},
		{"depth 5 parent with max=5 - exceeds", "bd-abc123.1.2.3.4.5", 5, true, "maximum hierarchy depth (5) exceeded for parent bd-abc123.1.2.3.4.5"},

		// Negative maxDepth falls back to default
		{"negative maxDepth uses default", "bd-abc123.1.2.3", -1, true, "maximum hierarchy depth (3) exceeded for parent bd-abc123.1.2.3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckHierarchyDepth(tt.parentID, tt.maxDepth)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
