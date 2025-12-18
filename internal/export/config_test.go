package export

import (
	"context"
	"testing"
)

// mockConfigStore implements ConfigStore for testing
type mockConfigStore struct {
	configs map[string]string
	err     error
}

func newMockConfigStore() *mockConfigStore {
	return &mockConfigStore{
		configs: make(map[string]string),
	}
}

func (m *mockConfigStore) GetConfig(ctx context.Context, key string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.configs[key], nil
}

func (m *mockConfigStore) SetConfig(ctx context.Context, key, value string) error {
	if m.err != nil {
		return m.err
	}
	m.configs[key] = value
	return nil
}

func TestLoadConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("returns defaults when no config", func(t *testing.T) {
		store := newMockConfigStore()
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Policy != DefaultErrorPolicy {
			t.Errorf("Policy = %v, want %v", cfg.Policy, DefaultErrorPolicy)
		}
		if cfg.RetryAttempts != DefaultRetryAttempts {
			t.Errorf("RetryAttempts = %v, want %v", cfg.RetryAttempts, DefaultRetryAttempts)
		}
		if cfg.RetryBackoffMS != DefaultRetryBackoffMS {
			t.Errorf("RetryBackoffMS = %v, want %v", cfg.RetryBackoffMS, DefaultRetryBackoffMS)
		}
		if cfg.SkipEncodingErrors != DefaultSkipEncodingErrors {
			t.Errorf("SkipEncodingErrors = %v, want %v", cfg.SkipEncodingErrors, DefaultSkipEncodingErrors)
		}
		if cfg.WriteManifest != DefaultWriteManifest {
			t.Errorf("WriteManifest = %v, want %v", cfg.WriteManifest, DefaultWriteManifest)
		}
	})

	t.Run("loads custom policy", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyErrorPolicy] = string(PolicyBestEffort)
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Policy != PolicyBestEffort {
			t.Errorf("Policy = %v, want %v", cfg.Policy, PolicyBestEffort)
		}
	})

	t.Run("loads auto-export policy when isAutoExport", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyAutoExportPolicy] = string(PolicyPartial)
		cfg, err := LoadConfig(ctx, store, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Policy != PolicyPartial {
			t.Errorf("Policy = %v, want %v", cfg.Policy, PolicyPartial)
		}
		if !cfg.IsAutoExport {
			t.Error("IsAutoExport should be true")
		}
	})

	t.Run("loads retry attempts", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyRetryAttempts] = "5"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RetryAttempts != 5 {
			t.Errorf("RetryAttempts = %v, want 5", cfg.RetryAttempts)
		}
	})

	t.Run("loads retry backoff", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyRetryBackoffMS] = "200"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RetryBackoffMS != 200 {
			t.Errorf("RetryBackoffMS = %v, want 200", cfg.RetryBackoffMS)
		}
	})

	t.Run("loads skip encoding errors", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeySkipEncodingErrors] = "true"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.SkipEncodingErrors {
			t.Error("SkipEncodingErrors should be true")
		}
	})

	t.Run("loads write manifest", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyWriteManifest] = "true"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.WriteManifest {
			t.Error("WriteManifest should be true")
		}
	})

	t.Run("ignores invalid policy", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyErrorPolicy] = "invalid-policy"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Policy != DefaultErrorPolicy {
			t.Errorf("Policy = %v, want %v (default)", cfg.Policy, DefaultErrorPolicy)
		}
	})

	t.Run("ignores invalid retry attempts", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyRetryAttempts] = "not-a-number"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RetryAttempts != DefaultRetryAttempts {
			t.Errorf("RetryAttempts = %v, want %v (default)", cfg.RetryAttempts, DefaultRetryAttempts)
		}
	})

	t.Run("ignores negative retry attempts", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyRetryAttempts] = "-1"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RetryAttempts != DefaultRetryAttempts {
			t.Errorf("RetryAttempts = %v, want %v (default)", cfg.RetryAttempts, DefaultRetryAttempts)
		}
	})

	t.Run("ignores invalid retry backoff", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyRetryBackoffMS] = "not-a-number"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RetryBackoffMS != DefaultRetryBackoffMS {
			t.Errorf("RetryBackoffMS = %v, want %v (default)", cfg.RetryBackoffMS, DefaultRetryBackoffMS)
		}
	})

	t.Run("ignores zero retry backoff", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyRetryBackoffMS] = "0"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RetryBackoffMS != DefaultRetryBackoffMS {
			t.Errorf("RetryBackoffMS = %v, want %v (default)", cfg.RetryBackoffMS, DefaultRetryBackoffMS)
		}
	})

	t.Run("ignores invalid skip encoding errors", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeySkipEncodingErrors] = "not-a-bool"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.SkipEncodingErrors != DefaultSkipEncodingErrors {
			t.Errorf("SkipEncodingErrors = %v, want %v (default)", cfg.SkipEncodingErrors, DefaultSkipEncodingErrors)
		}
	})

	t.Run("ignores invalid write manifest", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyWriteManifest] = "not-a-bool"
		cfg, err := LoadConfig(ctx, store, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.WriteManifest != DefaultWriteManifest {
			t.Errorf("WriteManifest = %v, want %v (default)", cfg.WriteManifest, DefaultWriteManifest)
		}
	})

	t.Run("falls back to general policy if auto-export not set", func(t *testing.T) {
		store := newMockConfigStore()
		store.configs[ConfigKeyErrorPolicy] = string(PolicyBestEffort)
		cfg, err := LoadConfig(ctx, store, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Policy != PolicyBestEffort {
			t.Errorf("Policy = %v, want %v", cfg.Policy, PolicyBestEffort)
		}
	})
}
