package export

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRetryWithBackoff(t *testing.T) {
	ctx := context.Background()

	t.Run("succeeds first try", func(t *testing.T) {
		attempts := 0
		err := RetryWithBackoff(ctx, 3, 100, "test", func() error {
			attempts++
			return nil
		})
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if attempts != 1 {
			t.Errorf("expected 1 attempt, got %d", attempts)
		}
	})

	t.Run("succeeds after retries", func(t *testing.T) {
		attempts := 0
		err := RetryWithBackoff(ctx, 3, 10, "test", func() error {
			attempts++
			if attempts < 3 {
				return errors.New("transient error")
			}
			return nil
		})
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if attempts != 3 {
			t.Errorf("expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("fails after max retries", func(t *testing.T) {
		attempts := 0
		err := RetryWithBackoff(ctx, 3, 10, "test", func() error {
			attempts++
			return errors.New("persistent error")
		})
		if err == nil {
			t.Error("expected error, got nil")
		}
		if attempts != 3 {
			t.Errorf("expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		attempts := 0
		err := RetryWithBackoff(ctx, 10, 100, "test", func() error {
			attempts++
			return errors.New("error")
		})
		if err != context.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got %v", err)
		}
		// Should stop before reaching max retries due to timeout
		if attempts >= 10 {
			t.Errorf("expected fewer than 10 attempts due to timeout, got %d", attempts)
		}
	})
}

func TestErrorPolicy(t *testing.T) {
	tests := []struct {
		name  string
		policy ErrorPolicy
		valid bool
	}{
		{"strict", PolicyStrict, true},
		{"best-effort", PolicyBestEffort, true},
		{"partial", PolicyPartial, true},
		{"required-core", PolicyRequiredCore, true},
		{"invalid", ErrorPolicy("invalid"), false},
		{"empty", ErrorPolicy(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.IsValid(); got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestErrorPolicyString(t *testing.T) {
	tests := []struct {
		policy ErrorPolicy
		want   string
	}{
		{PolicyStrict, "strict"},
		{PolicyBestEffort, "best-effort"},
		{PolicyPartial, "partial"},
		{PolicyRequiredCore, "required-core"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.policy.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewManifest(t *testing.T) {
	manifest := NewManifest(PolicyBestEffort)

	if manifest == nil {
		t.Fatal("NewManifest returned nil")
	}
	if manifest.ErrorPolicy != string(PolicyBestEffort) {
		t.Errorf("ErrorPolicy = %v, want %v", manifest.ErrorPolicy, PolicyBestEffort)
	}
	if !manifest.Complete {
		t.Error("Complete should be true by default")
	}
	if manifest.ExportedAt.IsZero() {
		t.Error("ExportedAt should not be zero")
	}
}

func TestWriteManifest(t *testing.T) {
	t.Run("writes manifest successfully", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := tmpDir + "/test.jsonl"

		manifest := NewManifest(PolicyStrict)
		manifest.ExportedCount = 5
		manifest.Complete = true

		err := WriteManifest(jsonlPath, manifest)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify manifest file was created
		manifestPath := tmpDir + "/test.manifest.json"
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			t.Error("manifest file was not created")
		}
	})

	t.Run("fails on invalid directory", func(t *testing.T) {
		err := WriteManifest("/nonexistent/path/test.jsonl", NewManifest(PolicyStrict))
		if err == nil {
			t.Error("expected error for nonexistent directory")
		}
	})
}

func TestFetchWithPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("strict policy fails fast", func(t *testing.T) {
		cfg := &Config{
			Policy:         PolicyStrict,
			RetryAttempts:  1,
			RetryBackoffMS: 10,
		}
		result := FetchWithPolicy(ctx, cfg, DataTypeCore, "test", func() error {
			return errors.New("test error")
		})
		if result.Err == nil {
			t.Error("expected error, got nil")
		}
		if result.Success {
			t.Error("expected Success=false")
		}
	})

	t.Run("best-effort policy skips errors", func(t *testing.T) {
		cfg := &Config{
			Policy:         PolicyBestEffort,
			RetryAttempts:  1,
			RetryBackoffMS: 10,
		}
		result := FetchWithPolicy(ctx, cfg, DataTypeLabels, "test", func() error {
			return errors.New("test error")
		})
		if result.Err != nil {
			t.Errorf("expected no error in best-effort, got %v", result.Err)
		}
		if result.Success {
			t.Error("expected Success=false")
		}
		if len(result.Warnings) == 0 {
			t.Error("expected warnings")
		}
	})

	t.Run("required-core fails on core data", func(t *testing.T) {
		cfg := &Config{
			Policy:         PolicyRequiredCore,
			RetryAttempts:  1,
			RetryBackoffMS: 10,
		}
		result := FetchWithPolicy(ctx, cfg, DataTypeCore, "test", func() error {
			return errors.New("test error")
		})
		if result.Err == nil {
			t.Error("expected error for core data, got nil")
		}
		if result.Success {
			t.Error("expected Success=false")
		}
	})

	t.Run("required-core skips enrichment errors", func(t *testing.T) {
		cfg := &Config{
			Policy:         PolicyRequiredCore,
			RetryAttempts:  1,
			RetryBackoffMS: 10,
		}
		result := FetchWithPolicy(ctx, cfg, DataTypeLabels, "test", func() error {
			return errors.New("test error")
		})
		if result.Err != nil {
			t.Errorf("expected no error for enrichment, got %v", result.Err)
		}
		if result.Success {
			t.Error("expected Success=false")
		}
		if len(result.Warnings) == 0 {
			t.Error("expected warnings")
		}
	})
}
