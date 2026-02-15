package export

import (
	"context"
	"fmt"
	"strconv"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// ConfigStore defines the minimal storage interface needed for config
type ConfigStore interface {
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
}

// LoadConfig reads export configuration from storage
func LoadConfig(ctx context.Context, store ConfigStore, isAutoExport bool) (*Config, error) {
	cfg := &Config{
		Policy:             DefaultErrorPolicy,
		RetryAttempts:      DefaultRetryAttempts,
		RetryBackoffMS:     DefaultRetryBackoffMS,
		SkipEncodingErrors: DefaultSkipEncodingErrors,
		WriteManifest:      DefaultWriteManifest,
		IsAutoExport:       isAutoExport,
	}

	// Load error policy
	if isAutoExport {
		// Check auto-export specific policy first
		if val, err := store.GetConfig(ctx, ConfigKeyAutoExportPolicy); err == nil && val != "" {
			policy := ErrorPolicy(val)
			if policy.IsValid() {
				cfg.Policy = policy
			}
		}
	}
	// Fall back to general export policy if not set or not auto-export
	if cfg.Policy == DefaultErrorPolicy {
		if val, err := store.GetConfig(ctx, ConfigKeyErrorPolicy); err == nil && val != "" {
			policy := ErrorPolicy(val)
			if policy.IsValid() {
				cfg.Policy = policy
			}
		}
	}

	// Load retry attempts
	if val, err := store.GetConfig(ctx, ConfigKeyRetryAttempts); err == nil && val != "" {
		if attempts, err := strconv.Atoi(val); err == nil && attempts >= 0 {
			cfg.RetryAttempts = attempts
		}
	}

	// Load retry backoff
	if val, err := store.GetConfig(ctx, ConfigKeyRetryBackoffMS); err == nil && val != "" {
		if backoff, err := strconv.Atoi(val); err == nil && backoff > 0 {
			cfg.RetryBackoffMS = backoff
		}
	}

	// Load skip encoding errors flag
	if val, err := store.GetConfig(ctx, ConfigKeySkipEncodingErrors); err == nil && val != "" {
		if skip, err := strconv.ParseBool(val); err == nil {
			cfg.SkipEncodingErrors = skip
		}
	}

	// Load write manifest flag
	if val, err := store.GetConfig(ctx, ConfigKeyWriteManifest); err == nil && val != "" {
		if write, err := strconv.ParseBool(val); err == nil {
			cfg.WriteManifest = write
		}
	}

	return cfg, nil
}

// SetPolicy sets the error policy for exports
func SetPolicy(ctx context.Context, store *dolt.DoltStore, policy ErrorPolicy, autoExport bool) error {
	if !policy.IsValid() {
		return fmt.Errorf("invalid error policy: %s (valid: strict, best-effort, partial, required-core)", policy)
	}

	key := ConfigKeyErrorPolicy
	if autoExport {
		key = ConfigKeyAutoExportPolicy
	}

	return store.SetConfig(ctx, key, string(policy))
}

// SetRetryAttempts sets the number of retry attempts
func SetRetryAttempts(ctx context.Context, store *dolt.DoltStore, attempts int) error {
	if attempts < 0 {
		return fmt.Errorf("retry attempts must be non-negative")
	}
	return store.SetConfig(ctx, ConfigKeyRetryAttempts, strconv.Itoa(attempts))
}

// SetRetryBackoff sets the initial retry backoff in milliseconds
func SetRetryBackoff(ctx context.Context, store *dolt.DoltStore, backoffMS int) error {
	if backoffMS <= 0 {
		return fmt.Errorf("retry backoff must be positive")
	}
	return store.SetConfig(ctx, ConfigKeyRetryBackoffMS, strconv.Itoa(backoffMS))
}

// SetSkipEncodingErrors sets whether to skip issues with encoding errors
func SetSkipEncodingErrors(ctx context.Context, store *dolt.DoltStore, skip bool) error {
	return store.SetConfig(ctx, ConfigKeySkipEncodingErrors, strconv.FormatBool(skip))
}

// SetWriteManifest sets whether to write export manifests
func SetWriteManifest(ctx context.Context, store *dolt.DoltStore, write bool) error {
	return store.SetConfig(ctx, ConfigKeyWriteManifest, strconv.FormatBool(write))
}
