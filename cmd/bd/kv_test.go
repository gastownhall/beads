//go:build cgo

package main

import (
	"context"
	"strings"
	"testing"
)

func TestKVCommands(t *testing.T) {
	ctx := context.Background()
	testStore, cleanup := setupTestDB(t)
	defer cleanup()

	// Test set and get
	t.Run("set and get", func(t *testing.T) {
		key := "kv.test_key"
		value := "test_value"

		err := testStore.SetConfig(ctx, key, value)
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		got, err := testStore.GetConfig(ctx, key)
		if err != nil {
			t.Fatalf("GetConfig failed: %v", err)
		}
		if got != value {
			t.Errorf("Expected %q, got %q", value, got)
		}
	})

	// Test get nonexistent key
	t.Run("get nonexistent", func(t *testing.T) {
		got, err := testStore.GetConfig(ctx, "kv.nonexistent")
		if err != nil {
			t.Fatalf("GetConfig failed: %v", err)
		}
		if got != "" {
			t.Errorf("Expected empty string for nonexistent key, got %q", got)
		}
	})

	// Test update existing key
	t.Run("update existing", func(t *testing.T) {
		key := "kv.update_test"

		err := testStore.SetConfig(ctx, key, "original")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		err = testStore.SetConfig(ctx, key, "updated")
		if err != nil {
			t.Fatalf("SetConfig update failed: %v", err)
		}

		got, err := testStore.GetConfig(ctx, key)
		if err != nil {
			t.Fatalf("GetConfig failed: %v", err)
		}
		if got != "updated" {
			t.Errorf("Expected 'updated', got %q", got)
		}
	})

	// Test delete
	t.Run("delete", func(t *testing.T) {
		key := "kv.delete_test"

		err := testStore.SetConfig(ctx, key, "to_delete")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		err = testStore.DeleteConfig(ctx, key)
		if err != nil {
			t.Fatalf("DeleteConfig failed: %v", err)
		}

		got, err := testStore.GetConfig(ctx, key)
		if err != nil {
			t.Fatalf("GetConfig after delete failed: %v", err)
		}
		if got != "" {
			t.Errorf("Expected empty string after delete, got %q", got)
		}
	})

	// Test delete nonexistent (should not error)
	t.Run("delete nonexistent", func(t *testing.T) {
		err := testStore.DeleteConfig(ctx, "kv.never_existed")
		if err != nil {
			t.Fatalf("DeleteConfig for nonexistent key should not error: %v", err)
		}
	})

	// Test list (GetAllConfig with kv prefix filtering)
	t.Run("list kv pairs", func(t *testing.T) {
		// Set some KV pairs
		err := testStore.SetConfig(ctx, "kv.list_test_1", "value1")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}
		err = testStore.SetConfig(ctx, "kv.list_test_2", "value2")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		// Also set a non-KV config to ensure filtering works
		err = testStore.SetConfig(ctx, "other.config", "should_not_appear")
		if err != nil {
			t.Fatalf("SetConfig failed: %v", err)
		}

		allConfig, err := testStore.GetAllConfig(ctx)
		if err != nil {
			t.Fatalf("GetAllConfig failed: %v", err)
		}

		// Filter for kv.* keys (mimicking kvListCmd logic)
		kvCount := 0
		for k := range allConfig {
			if len(k) > 3 && k[:3] == "kv." {
				kvCount++
			}
		}

		if kvCount < 2 {
			t.Errorf("Expected at least 2 kv.* entries, got %d", kvCount)
		}

		// Verify non-KV config exists but would be filtered
		if allConfig["other.config"] != "should_not_appear" {
			t.Errorf("Expected other.config to exist in raw config")
		}
	})

	// Test special characters in values
	t.Run("special characters", func(t *testing.T) {
		testCases := []struct {
			name  string
			value string
		}{
			{"url", "https://api.example.com/path?query=1&other=2"},
			{"json", `{"key": "value", "num": 42}`},
			{"multiline", "line1\nline2\nline3"},
			{"unicode", "Hello \u4e16\u754c"},
			{"empty", ""},
			{"spaces", "  leading and trailing  "},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				key := "kv.special_" + tc.name
				err := testStore.SetConfig(ctx, key, tc.value)
				if err != nil {
					t.Fatalf("SetConfig failed for %s: %v", tc.name, err)
				}

				got, err := testStore.GetConfig(ctx, key)
				if err != nil {
					t.Fatalf("GetConfig failed for %s: %v", tc.name, err)
				}
				if got != tc.value {
					t.Errorf("Expected %q, got %q", tc.value, got)
				}
			})
		}
	})
}

func TestKVPrefix(t *testing.T) {
	// Verify the kvPrefix constant matches expected value
	if kvPrefix != "kv." {
		t.Errorf("Expected kvPrefix to be 'kv.', got %q", kvPrefix)
	}
}

func TestValidateKVKey(t *testing.T) {
	testCases := []struct {
		name    string
		key     string
		wantErr bool
		errMsg  string
	}{
		// Valid keys
		{"simple key", "mykey", false, ""},
		{"key with underscore", "my_key", false, ""},
		{"key with dots", "my.key.name", false, ""},
		{"key with numbers", "key123", false, ""},

		// Invalid keys
		{"empty key", "", true, "cannot be empty"},
		{"whitespace only", "   ", true, "cannot be only whitespace"},
		{"kv prefix", "kv.nested", true, "cannot start with 'kv.'"},
		{"sync prefix", "sync.mode", true, "reserved prefix"},
		{"conflict prefix", "conflict.strategy", true, "reserved prefix"},
		{"federation prefix", "federation.remote", true, "reserved prefix"},
		{"jira prefix", "jira.url", true, "reserved prefix"},
		{"linear prefix", "linear.key", true, "reserved prefix"},
		{"export prefix", "export.path", true, "reserved prefix"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateKVKey(tc.key)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Expected error for key %q, got nil", tc.key)
				} else if tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("Expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for key %q: %v", tc.key, err)
				}
			}
		})
	}
}
