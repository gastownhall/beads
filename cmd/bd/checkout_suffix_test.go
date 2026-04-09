package main

import (
	"path/filepath"
	"testing"
)

func TestGenerateCheckoutSuffix(t *testing.T) {
	t.Parallel()

	// Generate several suffixes and verify format
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s, err := generateCheckoutSuffix()
		if err != nil {
			t.Fatalf("generateCheckoutSuffix() error: %v", err)
		}
		if len(s) != 3 {
			t.Errorf("expected 3-char suffix, got %q (len %d)", s, len(s))
		}
		if err := validateCheckoutSuffix(s); err != nil {
			t.Errorf("generated suffix %q failed validation: %v", s, err)
		}
		seen[s] = true
	}
	// With 46656 possible values and 100 samples, we should see at least 90 unique
	if len(seen) < 90 {
		t.Errorf("expected high uniqueness in 100 samples, got %d unique values", len(seen))
	}
}

func TestComputeCheckoutID(t *testing.T) {
	t.Parallel()

	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()
		id1 := computeCheckoutID("/home/user/project/.beads")
		id2 := computeCheckoutID("/home/user/project/.beads")
		if id1 != id2 {
			t.Errorf("same path produced different IDs: %q vs %q", id1, id2)
		}
	})

	t.Run("different paths produce different IDs", func(t *testing.T) {
		t.Parallel()
		id1 := computeCheckoutID("/home/user/project-a/.beads")
		id2 := computeCheckoutID("/home/user/project-b/.beads")
		if id1 == id2 {
			t.Errorf("different paths produced same ID: %q", id1)
		}
	})

	t.Run("8 hex chars", func(t *testing.T) {
		t.Parallel()
		id := computeCheckoutID("/some/path/.beads")
		if len(id) != 8 {
			t.Errorf("expected 8-char ID, got %q (len %d)", id, len(id))
		}
	})

	t.Run("empty beadsDir returns empty", func(t *testing.T) {
		t.Parallel()
		if id := computeCheckoutID(""); id != "" {
			t.Errorf("expected empty ID for empty beadsDir, got %q", id)
		}
	})

	t.Run("uses parent directory", func(t *testing.T) {
		t.Parallel()
		// /home/user/project/.beads → parent is /home/user/project
		// /home/user/project/.beads/sub → parent is /home/user/project/.beads
		// These should differ because the parent differs
		id1 := computeCheckoutID("/home/user/project/.beads")
		id2 := computeCheckoutID(filepath.Join("/home/user/project/.beads", "sub"))
		if id1 == id2 {
			t.Errorf("different parent dirs produced same ID: %q", id1)
		}
	})
}

func TestValidateCheckoutSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid 3-char", "k9x", false},
		{"valid 1-char", "a", false},
		{"valid 8-char", "abcd1234", false},
		{"valid with trailing hyphen stripped", "k9x-", false},
		{"valid starts with number", "9ax", false},

		{"empty", "", true},
		{"too long", "abcdefghi", true},
		{"uppercase", "K9x", true},
		{"contains hyphen", "k-x", true},
		{"contains underscore", "k_x", true},
		{"only hyphen", "-", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateCheckoutSuffix(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCheckoutSuffix(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
